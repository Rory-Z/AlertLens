package alertlens_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
)

const (
	firingTimeout  = 2 * time.Minute
	holmesTimeout  = 20 * time.Minute
	humanTimeout   = 10 * time.Minute
	resolveTimeout = 7 * time.Minute
	pollInterval   = 10 * time.Second
)

type syntheticAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

type slackBot struct {
	userID string
	botID  string
}

type threadState struct {
	parent slackapi.Message
	reply  slackapi.Message
}

type resolvedState struct {
	original slackapi.Message
	resolved slackapi.Message
	replied  bool
}

func TestE2E(t *testing.T) {
	if os.Getenv("ALERTLENS_E2E") != "1" {
		t.Skip("run with make e2e-test")
	}
	alertmanagerURL := requiredEnv(t, "ALERTMANAGER_URL")
	channel := requiredEnv(t, "E2E_SLACK_CHANNEL")
	slack := slackapi.New(requiredEnv(t, "SLACK_BOT_TOKEN"))

	preflightCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	auth, err := slack.AuthTestContext(preflightCtx)
	if err == nil {
		_, err = slack.GetConversationHistoryContext(preflightCtx, &slackapi.GetConversationHistoryParameters{
			ChannelID: channel, Limit: 1,
		})
	}
	cancel()
	if err != nil {
		t.Fatalf("Slack preflight: %v", err)
	}
	bot := slackBot{userID: auth.UserID, botID: auth.BotID}

	now := time.Now().UTC()
	runID := strconv.FormatInt(now.UnixNano(), 36)
	alert := syntheticAlert{
		Labels: map[string]string{
			"alertname": "AlertLensE2E", "namespace": "alertlens-e2e-" + runID,
			"severity": "warning", "e2e": "true",
		},
		Annotations: map[string]string{
			"summary":     "AlertLens synthetic E2E alert " + runID,
			"description": "Synthetic E2E alert — no operator action required.",
		},
		StartsAt: now, EndsAt: now.Add(time.Hour),
	}

	resolved := false
	t.Cleanup(func() {
		if resolved {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		alert.EndsAt = time.Now().UTC().Add(-time.Second)
		if err := postAlert(ctx, alertmanagerURL, alert); err != nil {
			t.Logf("cleanup synthetic alert: %v", err)
		}
	})

	injectCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = postAlert(injectCtx, alertmanagerURL, alert)
	cancel()
	if err != nil {
		t.Fatalf("inject firing alert: %v", err)
	}
	t.Logf("injected synthetic alert %s", runID)

	oldest := slackTimestamp(now.Add(-time.Second))
	parent := waitFor(t, "firing Slack notification", firingTimeout, func(ctx context.Context) (slackapi.Message, bool, error) {
		messages, err := channelHistory(ctx, slack, channel, oldest)
		if err != nil {
			return slackapi.Message{}, false, err
		}
		for _, message := range messages {
			if strings.Contains(messageContent(message), runID) {
				return message, true, nil
			}
		}
		return slackapi.Message{}, false, nil
	})

	permalinkCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	permalink, err := slack.GetPermalinkContext(permalinkCtx, &slackapi.PermalinkParameters{
		Channel: channel, Ts: parent.Timestamp,
	})
	cancel()
	if err != nil {
		t.Fatalf("get Slack permalink: %v", err)
	}

	firing := waitFor(t, "AlertLens RCA", holmesTimeout, func(ctx context.Context) (threadState, bool, error) {
		return completedThread(ctx, slack, channel, parent.Timestamp, parent.Timestamp, bot)
	})
	if reactedBy(firing.parent, "x", bot) {
		t.Fatal("AlertLens marked the firing alert as failed")
	}

	t.Logf("ACTION REQUIRED\nOpen Slack thread: %s\nIn that thread, mention @%s and include this exact text: E2E %s: Reply with a one-sentence summary of this synthetic alert.\nWaiting up to 10 minutes...", permalink, auth.User, runID)
	question := waitFor(t, "operator Slack mention", humanTimeout, func(ctx context.Context) (slackapi.Message, bool, error) {
		messages, err := threadReplies(ctx, slack, channel, parent.Timestamp)
		if err != nil {
			return slackapi.Message{}, false, err
		}
		for _, message := range messages {
			if message.Timestamp > firing.reply.Timestamp && !isBotMessage(message, bot) &&
				strings.Contains(messageContent(message), runID) {
				return message, true, nil
			}
		}
		return slackapi.Message{}, false, nil
	})

	followup := waitFor(t, "AlertLens follow-up", holmesTimeout, func(ctx context.Context) (threadState, bool, error) {
		return completedThread(ctx, slack, channel, parent.Timestamp, question.Timestamp, bot)
	})
	if reactedBy(followup.parent, "x", bot) {
		t.Fatal("AlertLens marked the follow-up as failed")
	}

	resolveStarted := time.Now().UTC()
	alert.EndsAt = resolveStarted.Add(-time.Second)
	resolveCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = postAlert(resolveCtx, alertmanagerURL, alert)
	cancel()
	if err != nil {
		t.Fatalf("resolve synthetic alert: %v", err)
	}
	resolved = true

	completeOnPreviousPoll := false
	final := waitFor(t, "AlertLens resolution", resolveTimeout, func(ctx context.Context) (resolvedState, bool, error) {
		history, err := channelHistory(ctx, slack, channel, slackTimestamp(resolveStarted.Add(-time.Second)))
		if err != nil {
			return resolvedState{}, false, err
		}
		var resolvedParent slackapi.Message
		for _, message := range history {
			if message.Timestamp != parent.Timestamp && strings.Contains(messageContent(message), runID) {
				resolvedParent = message
				break
			}
		}
		if resolvedParent.Timestamp == "" {
			return resolvedState{}, false, nil
		}
		replies, err := threadReplies(ctx, slack, channel, parent.Timestamp)
		if err != nil {
			return resolvedState{}, false, err
		}
		state := resolvedState{
			original: messageAt(replies, parent.Timestamp), resolved: resolvedParent,
		}
		for _, message := range replies {
			if isBotMessage(message, bot) && strings.Contains(message.Text, "Alertmanager confirms this alert is resolved.") {
				state.replied = true
				break
			}
		}
		failed := reactedBy(state.resolved, "x", bot)
		done := state.replied && reactedBy(state.original, "large_green_circle", bot) &&
			reactedBy(state.resolved, "large_green_circle", bot)
		if failed {
			return state, true, nil
		}
		if !done {
			completeOnPreviousPoll = false
			return state, false, nil
		}
		if completeOnPreviousPoll {
			return state, true, nil
		}
		completeOnPreviousPoll = true
		return state, false, nil
	})
	if reactedBy(final.resolved, "x", bot) {
		t.Fatal("AlertLens marked the resolved alert as failed")
	}
	t.Logf("E2E passed: %s", permalink)
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func postAlert(ctx context.Context, baseURL string, alert syntheticAlert) error {
	endpoint, err := url.JoinPath(baseURL, "api/v2/alerts")
	if err != nil {
		return err
	}
	body, err := json.Marshal([]syntheticAlert{alert})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("Alertmanager returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

func channelHistory(ctx context.Context, api *slackapi.Client, channel, oldest string) ([]slackapi.Message, error) {
	response, err := api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
		ChannelID: channel, Oldest: oldest, Inclusive: true, Limit: 100,
	})
	if err != nil {
		return nil, err
	}
	return response.Messages, nil
}

func threadReplies(ctx context.Context, api *slackapi.Client, channel, timestamp string) ([]slackapi.Message, error) {
	messages, _, _, err := api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
		ChannelID: channel, Timestamp: timestamp, Limit: 100,
	})
	return messages, err
}

func completedThread(ctx context.Context, api *slackapi.Client, channel, threadTimestamp, eventTimestamp string, bot slackBot) (threadState, bool, error) {
	messages, err := threadReplies(ctx, api, channel, threadTimestamp)
	if err != nil {
		return threadState{}, false, err
	}
	state := threadState{parent: messageAt(messages, eventTimestamp)}
	state.reply, _ = botReplyAfter(messages, bot, eventTimestamp)
	done := reactedBy(state.parent, "x", bot) ||
		(state.reply.Timestamp != "" && reactedBy(state.parent, "white_check_mark", bot))
	return state, done, nil
}

func waitFor[T any](t *testing.T, name string, timeout time.Duration, check func(context.Context) (T, bool, error)) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var lastErr error
	for {
		value, done, err := check(ctx)
		if err == nil && done {
			return value
		}
		if err != nil {
			lastErr = err
		}
		delay := pollInterval
		var limited *slackapi.RateLimitedError
		if errors.As(err, &limited) && limited.RetryAfter > delay {
			delay = limited.RetryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				t.Fatalf("wait for %s: %v (last error: %v)", name, ctx.Err(), lastErr)
			}
			t.Fatalf("wait for %s: %v", name, ctx.Err())
		case <-timer.C:
		}
	}
}

func slackTimestamp(value time.Time) string {
	return fmt.Sprintf("%d.000000", value.Unix())
}

func messageAt(messages []slackapi.Message, timestamp string) slackapi.Message {
	for _, message := range messages {
		if message.Timestamp == timestamp {
			return message
		}
	}
	return slackapi.Message{}
}

func botReplyAfter(messages []slackapi.Message, bot slackBot, timestamp string) (slackapi.Message, bool) {
	for _, message := range messages {
		if message.Timestamp > timestamp && isBotMessage(message, bot) {
			return message, true
		}
	}
	return slackapi.Message{}, false
}

func isBotMessage(message slackapi.Message, bot slackBot) bool {
	return message.User == bot.userID || (bot.botID != "" && message.BotID == bot.botID)
}

func reactedBy(message slackapi.Message, reaction string, bot slackBot) bool {
	for _, item := range message.Reactions {
		if item.Name == reaction && slices.Contains(item.Users, bot.userID) {
			return true
		}
	}
	return false
}

func messageContent(message slackapi.Message) string {
	parts := []string{message.Text}
	for _, attachment := range message.Attachments {
		parts = append(parts, attachment.Fallback, attachment.Pretext, attachment.Title, attachment.Text)
		for _, field := range attachment.Fields {
			parts = append(parts, field.Title, field.Value)
		}
	}
	return strings.Join(parts, "\n")
}
