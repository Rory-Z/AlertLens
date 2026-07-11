package slackadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/emqx/alertlens/internal/service"
)

func TestTranslateMessage(t *testing.T) {
	event := slackevents.EventsAPIEvent{
		Type: slackevents.CallbackEvent,
		Data: &slackevents.EventsAPICallbackEvent{EventID: "Ev1"},
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
			User: "U1", Text: "FIRING HighCPU", TimeStamp: "100.1", ThreadTimeStamp: "99.1",
			Channel: "C1", BotID: "B1", SubType: "bot_message",
			Message: &slackapi.Msg{Attachments: []slackapi.Attachment{
				{Text: `<!-- alertlens:alertname=HighCPU,namespace=prod -->`},
				{Text: "details"},
			}},
		}},
	}
	got, ok := translate(event, map[string]bool{"C1": true}, "U_SELF")
	if !ok {
		t.Fatal("event rejected")
	}
	want := service.Event{
		ID: "Ev1", Channel: "C1", User: "U1", BotID: "B1",
		Text: "FIRING HighCPU\n<!-- alertlens:alertname=HighCPU,namespace=prod -->\ndetails",
		TS:   "100.1", ThreadTS: "99.1",
	}
	if got != want {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
}

func TestNewBuildsRealSocketClient(t *testing.T) {
	client := New("xoxb-test", "xapp-test", map[string]bool{"C1": true})
	socket, ok := client.socket.(realSocket)
	if !ok || client.api == nil || socket.Events() == nil || !client.channels["C1"] {
		t.Fatalf("client = %#v", client)
	}
}

func TestTranslateUsesNestedMessageFallback(t *testing.T) {
	event := slackevents.EventsAPIEvent{
		Type: slackevents.CallbackEvent,
		Data: slackevents.EventsAPICallbackEvent{EventID: "Ev2"},
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
			User: "U1", TimeStamp: "1", Channel: "C1", Message: &slackapi.Msg{Text: "fallback"},
		}},
	}
	got, ok := translate(event, map[string]bool{"C1": true}, "U_SELF")
	if !ok || got.ID != "Ev2" || got.Text != "fallback" {
		t.Fatalf("translate() = (%#v, %v)", got, ok)
	}
	event.InnerEvent.Data.(*slackevents.MessageEvent).Message.Text = ""
	if _, ok := translate(event, map[string]bool{"C1": true}, "U_SELF"); ok {
		t.Fatal("empty event accepted")
	}
}

func TestTranslateAppMention(t *testing.T) {
	for _, tt := range []struct {
		name     string
		threadTS string
	}{
		{name: "top level"},
		{name: "threaded", threadTS: "9.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := slackevents.EventsAPIEvent{
				Type: slackevents.CallbackEvent,
				Data: &slackevents.EventsAPICallbackEvent{EventID: "EvMention"},
				InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
					User: "U1", Text: "<@U_SELF> investigate prod", TimeStamp: "10.1",
					ThreadTimeStamp: tt.threadTS, Channel: "C1",
				}},
			}
			got, ok := translate(event, map[string]bool{"C1": true}, "U_SELF")
			if !ok || !got.Mention || got.ID != "EvMention" || got.Channel != "C1" ||
				got.User != "U1" || got.Text != "investigate prod" || got.TS != "10.1" || got.ThreadTS != tt.threadTS {
				t.Fatalf("translate() = (%#v, %v)", got, ok)
			}
		})
	}
}

func TestTranslateMentionBoundary(t *testing.T) {
	base := func() slackevents.EventsAPIEvent {
		return slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
			User: "U1", Text: "<@U_SELF>", TimeStamp: "1", Channel: "C1",
		}}}
	}
	t.Run("attachment only", func(t *testing.T) {
		event := base()
		event.InnerEvent.Data.(*slackevents.AppMentionEvent).Attachments = []slackapi.Attachment{{Text: "question"}}
		got, ok := translate(event, map[string]bool{"C1": true}, "U_SELF")
		if !ok || got.Text != "question" || got.ID != "" {
			t.Fatalf("translate() = (%#v, %v)", got, ok)
		}
	})
	for _, tt := range []struct {
		name   string
		mutate func(*slackevents.AppMentionEvent)
	}{
		{name: "unmonitored", mutate: func(e *slackevents.AppMentionEvent) { e.Channel = "C2" }},
		{name: "self", mutate: func(e *slackevents.AppMentionEvent) { e.User = "U_SELF" }},
		{name: "missing timestamp", mutate: func(e *slackevents.AppMentionEvent) { e.TimeStamp = "" }},
		{name: "empty", mutate: func(*slackevents.AppMentionEvent) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := base()
			tt.mutate(event.InnerEvent.Data.(*slackevents.AppMentionEvent))
			if _, ok := translate(event, map[string]bool{"C1": true}, "U_SELF"); ok {
				t.Fatal("event accepted")
			}
		})
	}
}

func TestTranslateRejectsIrrelevantMessages(t *testing.T) {
	base := func() slackevents.EventsAPIEvent {
		return slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			Data: &slackevents.EventsAPICallbackEvent{EventID: "Ev1"},
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
				User: "U1", Text: "hello", TimeStamp: "1", Channel: "C1",
			}},
		}
	}
	t.Run("unmonitored channel", func(t *testing.T) {
		if _, ok := translate(base(), map[string]bool{"C2": true}, "U_SELF"); ok {
			t.Fatal("event accepted")
		}
	})
	t.Run("self", func(t *testing.T) {
		if _, ok := translate(base(), map[string]bool{"C1": true}, "U1"); ok {
			t.Fatal("event accepted")
		}
	})
	t.Run("edited subtype", func(t *testing.T) {
		event := base()
		event.InnerEvent.Data.(*slackevents.MessageEvent).SubType = "message_changed"
		if _, ok := translate(event, map[string]bool{"C1": true}, "U_SELF"); ok {
			t.Fatal("event accepted")
		}
	})
	t.Run("wrong event", func(t *testing.T) {
		event := base()
		event.InnerEvent.Data = &slackevents.AppMentionEvent{}
		if _, ok := translate(event, map[string]bool{"C1": true}, "U_SELF"); ok {
			t.Fatal("event accepted")
		}
	})
}

func TestWebAPIOperations(t *testing.T) {
	requests := make(chan url.Values, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		requests <- r.Form
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/chat.postMessage" {
			_, _ = io.WriteString(w, `{"ok":true,"channel":"C1","ts":"2","message":{"text":"answer"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	client := &Client{api: slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/api/"))}
	ctx := context.Background()
	if err := client.AddReaction(ctx, "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveReaction(ctx, "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := client.Reply(ctx, "C1", "1", "answer"); err != nil {
		t.Fatal(err)
	}
	add, remove, reply := <-requests, <-requests, <-requests
	if add.Get("name") != "eyes" || add.Get("channel") != "C1" || add.Get("timestamp") != "1" ||
		remove.Get("name") != "eyes" || reply.Get("thread_ts") != "1" || reply.Get("text") != "answer" {
		t.Fatalf("forms = %#v %#v %#v", add, remove, reply)
	}
}

func TestReactionIdempotencyErrorsAreSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errorName := "already_reacted"
		if r.URL.Path == "/api/reactions.remove" {
			errorName = "no_reaction"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errorName})
	}))
	defer server.Close()
	client := &Client{api: slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/api/"))}
	if err := client.AddReaction(context.Background(), "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveReaction(context.Background(), "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIOperationsRetryRateLimitOnce(t *testing.T) {
	var mu sync.Mutex
	attempts := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts[r.URL.Path]++
		attempt := attempts[r.URL.Path]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"ok":false,"error":"ratelimited"}`)
			return
		}
		if r.URL.Path == "/api/chat.postMessage" {
			_, _ = io.WriteString(w, `{"ok":true,"channel":"C1","ts":"2","message":{"text":"answer"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	client := &Client{api: slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/api/"))}
	ctx := context.Background()
	if err := client.AddReaction(ctx, "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveReaction(ctx, "eyes", "C1", "1"); err != nil {
		t.Fatal(err)
	}
	if err := client.Reply(ctx, "C1", "1", "answer"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/reactions.add", "/api/reactions.remove", "/api/chat.postMessage"} {
		if attempts[path] != 2 {
			t.Fatalf("%s attempts = %d", path, attempts[path])
		}
	}
}

func TestRetryRateLimitCancellationAndOrdinaryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retryRateLimit(ctx, func() error {
		attempts++
		return &slackapi.RateLimitedError{RetryAfter: time.Hour}
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
	want := errors.New("ordinary")
	attempts = 0
	err = retryRateLimit(context.Background(), func() error {
		attempts++
		return want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}

func TestRunAuthenticatesAndAcknowledgesBeforeSubmit(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth.test" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true,"user_id":"U_SELF"}`)
	}))
	defer apiServer.Close()
	socket := newFakeSocket()
	client := &Client{
		api:      slackapi.New("xoxb-test", slackapi.OptionAPIURL(apiServer.URL+"/api/")),
		socket:   socket,
		channels: map[string]bool{"C1": true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handled := make(chan service.Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, func(_ context.Context, event service.Event) bool {
			if !socket.wasAcked("env1") {
				t.Error("handler ran before ACK")
			}
			handled <- event
			return true
		})
	}()
	socket.events <- socketmode.Event{Type: socketmode.EventTypeConnected}
	socket.events <- socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			Data: &slackevents.EventsAPICallbackEvent{EventID: "Ev1"},
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
				User: "U1", Text: `<!-- alertlens:alertname=A,namespace= -->`, TimeStamp: "1", Channel: "C1",
			}},
		},
		Request: &socketmode.Request{EnvelopeID: "env1"},
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("event not handled")
	}
	if err := client.Ready(); err != nil {
		t.Fatalf("ready = %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.botUserID != "U_SELF" || client.Ready() == nil {
		t.Fatalf("bot user = %q, readiness = %v", client.botUserID, client.Ready())
	}
}

func TestRunReturnsAuthenticationAndSocketErrors(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		}))
		defer server.Close()
		client := &Client{
			api:    slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/api/")),
			socket: newFakeSocket(), channels: map[string]bool{"C1": true},
		}
		if err := client.Run(context.Background(), func(context.Context, service.Event) bool { return true }); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("socket", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ok":true,"user_id":"U_SELF"}`)
		}))
		defer server.Close()
		socket := newFakeSocket()
		socket.runErr = errors.New("connection failed")
		client := &Client{
			api:    slackapi.New("xoxb-test", slackapi.OptionAPIURL(server.URL+"/api/")),
			socket: socket, channels: map[string]bool{"C1": true},
		}
		if err := client.Run(context.Background(), func(context.Context, service.Event) bool { return true }); err == nil {
			t.Fatal("expected error")
		}
	})
}

type fakeSocket struct {
	events chan socketmode.Event
	mu     sync.Mutex
	acked  map[string]bool
	runErr error
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{events: make(chan socketmode.Event, 4), acked: make(map[string]bool)}
}

func (f *fakeSocket) Events() <-chan socketmode.Event { return f.events }

func (f *fakeSocket) AckCtx(_ context.Context, requestID string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked[requestID] = true
	return nil
}

func (f *fakeSocket) RunContext(ctx context.Context) error {
	if f.runErr != nil {
		return f.runErr
	}
	<-ctx.Done()
	return nil
}

func (f *fakeSocket) wasAcked(requestID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acked[requestID]
}
