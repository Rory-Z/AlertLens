package slackadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/emqx/alertlens/internal/service"
)

type socketConnection interface {
	Events() <-chan socketmode.Event
	AckCtx(context.Context, string, any) error
	RunContext(context.Context) error
}

type realSocket struct {
	*socketmode.Client
}

func (s realSocket) Events() <-chan socketmode.Event { return s.Client.Events }

type Client struct {
	api              *slackapi.Client
	socket           socketConnection
	monitoredChannel string
	botUserID        string
	botID            string
	connected        atomic.Bool
}

func New(botToken, appToken, monitoredChannel string) *Client {
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return &Client{
		api: api, socket: realSocket{socketmode.New(api)}, monitoredChannel: monitoredChannel,
	}
}

func (c *Client) Run(ctx context.Context, handler func(context.Context, service.Event) bool) error {
	auth, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("Slack auth.test: %w", err)
	}
	c.botUserID = auth.UserID
	c.botID = auth.BotID

	runCtx, cancel := context.WithCancel(ctx)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		c.consume(runCtx, handler)
	}()
	err = c.socket.RunContext(runCtx)
	cancel()
	<-eventsDone
	c.connected.Store(false)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Slack Socket Mode: %w", err)
	}
	return nil
}

func (c *Client) Ready() error {
	if !c.connected.Load() {
		return errors.New("Slack Socket Mode is disconnected")
	}
	return nil
}

func (c *Client) AddReaction(ctx context.Context, name, channel, ts string) error {
	err := retryRateLimit(ctx, func() error {
		return c.api.AddReactionContext(ctx, name, slackapi.NewRefToMessage(channel, ts))
	})
	return ignoreSlackError(err, "already_reacted")
}

func (c *Client) RemoveReaction(ctx context.Context, name, channel, ts string) error {
	err := retryRateLimit(ctx, func() error {
		return c.api.RemoveReactionContext(ctx, name, slackapi.NewRefToMessage(channel, ts))
	})
	return ignoreSlackError(err, "no_reaction")
}

func (c *Client) Reply(ctx context.Context, channel, threadTS, text string) error {
	return retryRateLimit(ctx, func() error {
		_, _, err := c.api.PostMessageContext(ctx, channel, slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(threadTS))
		return err
	})
}

func (c *Client) Post(ctx context.Context, channel, text string) (string, error) {
	_, timestamp, err := c.api.PostMessageContext(ctx, channel, slackapi.MsgOptionText(text, false))
	return timestamp, err
}

func (c *Client) Conversation(ctx context.Context, channel, threadTS, currentTS string) ([]service.ConversationMessage, error) {
	var all []slackapi.Message
	cursor := ""
	for {
		var messages []slackapi.Message
		var hasMore bool
		var next string
		err := retryRateLimit(ctx, func() error {
			var err error
			messages, hasMore, next, err = c.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
				ChannelID: channel, Timestamp: threadTS, Latest: currentTS, Limit: 200, Cursor: cursor,
			})
			return err
		})
		if err != nil {
			return nil, err
		}
		all = append(all, messages...)
		if !hasMore || next == "" {
			break
		}
		cursor = next
	}
	result := make([]service.ConversationMessage, 0, len(all))
	for _, message := range all {
		if message.Timestamp == currentTS {
			continue
		}
		text := messageText(message)
		switch {
		case message.Timestamp == threadTS:
			result = append(result, service.ConversationMessage{Role: "user", Content: text})
		case (message.User == c.botUserID || (c.botID != "" && message.BotID == c.botID)) && !failureReply(text):
			result = append(result, service.ConversationMessage{Role: "assistant", Content: text})
		case strings.Contains(text, "<@"+c.botUserID+">"):
			result = append(result, service.ConversationMessage{
				Role: "user", Content: strings.TrimSpace(strings.ReplaceAll(text, "<@"+c.botUserID+">", "")),
			})
		}
	}
	return result, nil
}

func failureReply(text string) bool {
	const legacyAlertmanagerFailureReplyPrefix = "⚠️ Alertmanager enrichment failed:"
	return strings.HasPrefix(text, service.AlertmanagerFailureReplyPrefix) ||
		strings.HasPrefix(text, legacyAlertmanagerFailureReplyPrefix) ||
		strings.HasPrefix(text, service.HolmesFailureReplyPrefix) ||
		strings.HasPrefix(text, service.HolmesAnswerDeliveryFailureReplyPrefix) ||
		strings.HasPrefix(text, service.ScheduledFailureReplyPrefix) ||
		text == service.ShutdownReply
}

func messageText(message slackapi.Message) string {
	parts := []string{message.Text}
	for _, attachment := range message.Attachments {
		parts = append(parts, attachment.Fallback, attachment.Pretext, attachment.Title, attachment.Text)
		for _, field := range attachment.Fields {
			parts = append(parts, field.Title, field.Value)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (c *Client) consume(ctx context.Context, handler func(context.Context, service.Event) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-c.socket.Events():
			switch event.Type {
			case socketmode.EventTypeConnected:
				c.connected.Store(true)
			case socketmode.EventTypeConnecting, socketmode.EventTypeConnectionError,
				socketmode.EventTypeDisconnect, socketmode.EventTypeInvalidAuth:
				c.connected.Store(false)
			case socketmode.EventTypeEventsAPI:
				if event.Request == nil || c.socket.AckCtx(ctx, event.Request.EnvelopeID, nil) != nil {
					continue
				}
				apiEvent, ok := event.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				if translated, ok := translate(apiEvent, c.monitoredChannel, c.botUserID); ok {
					handler(ctx, translated)
				}
			}
		}
	}
}

func translate(event slackevents.EventsAPIEvent, monitoredChannel, botUserID string) (service.Event, bool) {
	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		return translateMessage(inner, monitoredChannel, botUserID)
	case *slackevents.AppMentionEvent:
		if inner.Channel != monitoredChannel || inner.User == botUserID || inner.TimeStamp == "" {
			return service.Event{}, false
		}
		text := strings.TrimSpace(strings.ReplaceAll(inner.Text, "<@"+botUserID+">", ""))
		parts := make([]string, 0, 1+len(inner.Attachments))
		if text != "" {
			parts = append(parts, text)
		}
		for _, attachment := range inner.Attachments {
			if attachment.Text != "" {
				parts = append(parts, attachment.Text)
			}
		}
		if len(parts) == 0 {
			return service.Event{}, false
		}
		return service.Event{
			Channel: inner.Channel,
			Text:    strings.Join(parts, "\n"), TS: inner.TimeStamp, ThreadTS: inner.ThreadTimeStamp,
			Mention: true,
		}, true
	default:
		return service.Event{}, false
	}
}

func translateMessage(message *slackevents.MessageEvent, monitoredChannel, botUserID string) (service.Event, bool) {
	if (message.SubType != "" && message.SubType != "bot_message") ||
		message.BotID == "" || message.Channel != monitoredChannel || message.User == botUserID || message.TimeStamp == "" {
		return service.Event{}, false
	}
	parts := make([]string, 0, 3)
	if message.Text != "" {
		parts = append(parts, message.Text)
	} else if message.Message != nil && message.Message.Text != "" {
		parts = append(parts, message.Message.Text)
	}
	if message.Message != nil {
		for _, attachment := range message.Message.Attachments {
			if attachment.Text != "" {
				parts = append(parts, attachment.Text)
			}
		}
	}
	if len(parts) == 0 {
		return service.Event{}, false
	}
	return service.Event{
		Channel: message.Channel,
		Text:    strings.Join(parts, "\n"), TS: message.TimeStamp, ThreadTS: message.ThreadTimeStamp,
	}, true
}

func ignoreSlackError(err error, allowed string) error {
	if err == nil {
		return nil
	}
	var slackErr slackapi.SlackErrorResponse
	if errors.As(err, &slackErr) && slackErr.Err == allowed {
		return nil
	}
	return err
}

func retryRateLimit(ctx context.Context, operation func() error) error {
	err := operation()
	var rateLimited *slackapi.RateLimitedError
	if !errors.As(err, &rateLimited) {
		return err
	}
	timer := time.NewTimer(rateLimited.RetryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return operation()
	}
}
