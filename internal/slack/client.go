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
	api       *slackapi.Client
	socket    socketConnection
	channels  map[string]bool
	botUserID string
	connected atomic.Bool
}

func New(botToken, appToken string, channels map[string]bool) *Client {
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return &Client{
		api: api, socket: realSocket{socketmode.New(api)}, channels: channels,
	}
}

func (c *Client) Run(ctx context.Context, handler func(context.Context, service.Event) bool) error {
	auth, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("Slack auth.test: %w", err)
	}
	c.botUserID = auth.UserID

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

func (c *Client) consume(ctx context.Context, handler func(context.Context, service.Event) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-c.socket.Events():
			switch event.Type {
			case socketmode.EventTypeConnected:
				c.connected.Store(true)
			case socketmode.EventTypeConnectionError:
				c.connected.Store(false)
			case socketmode.EventTypeEventsAPI:
				if event.Request == nil || c.socket.AckCtx(ctx, event.Request.EnvelopeID, nil) != nil {
					continue
				}
				apiEvent, ok := event.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				if translated, ok := translate(apiEvent, c.channels, c.botUserID); ok {
					handler(ctx, translated)
				}
			}
		}
	}
}

func translate(event slackevents.EventsAPIEvent, channels map[string]bool, botUserID string) (service.Event, bool) {
	message, ok := event.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok || (message.SubType != "" && message.SubType != "bot_message") ||
		!channels[message.Channel] || message.User == botUserID || message.TimeStamp == "" {
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
	eventID := ""
	switch outer := event.Data.(type) {
	case *slackevents.EventsAPICallbackEvent:
		eventID = outer.EventID
	case slackevents.EventsAPICallbackEvent:
		eventID = outer.EventID
	}
	return service.Event{
		ID: eventID, Channel: message.Channel, User: message.User, BotID: message.BotID,
		Text: strings.Join(parts, "\n"), TS: message.TimeStamp, ThreadTS: message.ThreadTimeStamp,
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
