package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/session"
)

func TestFiringAlert(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state.json")
	store, err := session.Open(statePath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	alertmanagerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
          {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-0"},"annotations":{"runbook":"check cpu"},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prom/graph"},
          {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-1"},"annotations":{"runbook":"check cpu"},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prom/graph"}
        ]`)
	}))
	defer alertmanagerServer.Close()

	requestCh := make(chan holmes.Request, 1)
	holmesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Errorf("read persisted state before Holmes: %v", err)
		}
		var persisted session.Snapshot
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Errorf("decode persisted state before Holmes: %v", err)
		}
		if got := persisted.Sessions["am:HighCPU:prod"].State; got != "active" {
			t.Errorf("persisted state before Holmes = %q", got)
		}
		var request holmes.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Holmes request: %v", err)
		}
		requestCh <- request
		_, _ = io.WriteString(w, `{"analysis":"CPU saturation on api pods"}`)
	}))
	defer holmesServer.Close()

	slack := &fakeSlack{}
	service := New(
		store,
		alertmanager.New(testURL(t, alertmanagerServer.URL), time.Second),
		holmes.New(testURL(t, holmesServer.URL), time.Second),
		slack,
		Config{
			QueueSize:            10,
			Workers:              1,
			AlertSessionTTL:      24 * time.Hour,
			AlertPayloadMaxBytes: 32768,
			RunbookMaxBytes:      8192,
			ConversationMaxBytes: 16384,
			SlackOutputMaxChars:  2500,
		},
		func() time.Time { return now },
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	event := Event{
		ID:      "Ev1",
		Channel: "C1",
		Text:    "FIRING HighCPU\n<!-- alertlens:alertname=HighCPU,namespace=prod -->",
		TS:      "100.1",
	}
	if !service.Submit(ctx, event) {
		t.Fatal("event was not accepted")
	}
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:100.1") })

	wantReactions := []string{
		"add:eyes:C1:100.1",
		"remove:eyes:C1:100.1",
		"add:hourglass_flowing_sand:C1:100.1",
		"remove:hourglass_flowing_sand:C1:100.1",
		"add:white_check_mark:C1:100.1",
	}
	if got := slack.reactionLog(); !equalStrings(got, wantReactions) {
		t.Fatalf("reactions = %#v, want %#v", got, wantReactions)
	}
	request := <-requestCh
	if request.RequestSource != "alert_investigation" || request.SourceRef != "am:HighCPU:prod" ||
		request.ConversationID != "am:HighCPU:prod" || request.AdditionalSystemPrompt == "" {
		t.Fatalf("Holmes request metadata = %#v", request)
	}
	if !strings.Contains(request.Ask, `"pod":"api-0"`) || !strings.Contains(request.Ask, `"pod":"api-1"`) ||
		!strings.Contains(request.Ask, "<inline_runbooks>\ncheck cpu\n</inline_runbooks>") ||
		!strings.Contains(request.Ask, "<untrusted_slack_message>") {
		t.Fatalf("Holmes ask = %q", request.Ask)
	}
	replies := slack.replyLog()
	if len(replies) != 1 || replies[0] != "C1:100.1:CPU saturation on api pods" {
		t.Fatalf("replies = %#v", replies)
	}
	record := store.Snapshot().Sessions["am:HighCPU:prod"]
	if len(record.Conversation) != 1 || record.Conversation[0].Role != "assistant" ||
		record.Conversation[0].Content != "CPU saturation on api pods" {
		t.Fatalf("session = %#v", record)
	}
}

func TestDuplicateActiveAlertDoesNotRepeatRCA(t *testing.T) {
	var holmesCalls atomic.Int32
	slack := &fakeSlack{}
	service, _ := startBehaviorService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			return []alertmanager.Alert{{Labels: map[string]string{"alertname": "HighCPU", "namespace": "prod"}}}, nil
		}),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			holmesCalls.Add(1)
			return "answer", nil
		}),
		slack,
	)
	event := Event{Channel: "C1", Text: `<!-- alertlens:alertname=HighCPU,namespace=prod -->`, TS: "1"}
	if !service.Submit(context.Background(), event) {
		t.Fatal("first event rejected")
	}
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:1") })
	event.TS = "2"
	if !service.Submit(context.Background(), event) {
		t.Fatal("duplicate event rejected")
	}
	waitFor(t, func() bool { return slack.hasReaction("remove:eyes:C1:2") })
	if holmesCalls.Load() != 1 || len(slack.replyLog()) != 1 {
		t.Fatalf("Holmes calls = %d, replies = %#v", holmesCalls.Load(), slack.replyLog())
	}
}

func TestAlertmanagerFailureDoesNotCreateSession(t *testing.T) {
	slack := &fakeSlack{}
	service, store := startBehaviorService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			return nil, errors.New("Alertmanager unavailable")
		}),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			t.Fatal("Holmes must not be called")
			return "", nil
		}),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace= -->`, TS: "1"})
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	if len(store.Snapshot().Sessions) != 0 {
		t.Fatalf("sessions = %#v", store.Snapshot().Sessions)
	}
}

func TestHolmesFailureLeavesClaimedSessionWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	service, store := startBehaviorService(t,
		activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			calls.Add(1)
			return "", errors.New("Holmes unavailable")
		}),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace=ns -->`, TS: "1"})
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	if calls.Load() != 1 || store.Snapshot().Sessions["am:A:ns"].State != "active" {
		t.Fatalf("calls = %d, sessions = %#v", calls.Load(), store.Snapshot().Sessions)
	}
}

func TestWatchdogSkipsHolmesAndReply(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	service, _ := startBehaviorService(t,
		activeAlertmanager("Watchdog", ""),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			calls.Add(1)
			return "unexpected", nil
		}),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=Watchdog,namespace= -->`, TS: "1"})
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:1") })
	if calls.Load() != 0 || len(slack.replyLog()) != 0 {
		t.Fatalf("Holmes calls = %d, replies = %#v", calls.Load(), slack.replyLog())
	}
}

func TestQueueSaturationRejectsWithFailureReaction(t *testing.T) {
	slack := &fakeSlack{}
	service := New(nil, nil, nil, slack, Config{QueueSize: 1, Workers: 1}, time.Now)
	event := Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace= -->`, TS: "1"}
	if !service.Submit(context.Background(), event) {
		t.Fatal("first event rejected")
	}
	event.TS = "2"
	if service.Submit(context.Background(), event) {
		t.Fatal("saturated event accepted")
	}
	if !slack.hasReaction("add:x:C1:2") {
		t.Fatalf("reactions = %#v", slack.reactionLog())
	}
}

func TestSubmitIgnoresUnmarkedMessage(t *testing.T) {
	slack := &fakeSlack{}
	service := New(nil, nil, nil, slack, Config{QueueSize: 1, Workers: 1}, time.Now)
	if service.Submit(context.Background(), Event{Channel: "C1", Text: "hello", TS: "1"}) {
		t.Fatal("unmarked event accepted")
	}
	if len(slack.reactionLog()) != 0 {
		t.Fatalf("reactions = %#v", slack.reactionLog())
	}
}

func TestNoMatchingAlertRemovesReceivedReaction(t *testing.T) {
	slack := &fakeSlack{}
	service, _ := startBehaviorService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) { return nil, nil }),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			t.Fatal("Holmes must not be called")
			return "", nil
		}),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace= -->`, TS: "1"})
	waitFor(t, func() bool { return slack.hasReaction("remove:eyes:C1:1") })
	if len(slack.replyLog()) != 0 {
		t.Fatalf("replies = %#v", slack.replyLog())
	}
}

func TestReplyFailureEndsWithFailureReaction(t *testing.T) {
	slack := &fakeSlack{replyErr: errors.New("Slack unavailable")}
	service, store := startBehaviorService(t,
		activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) { return "answer", nil }),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace=ns -->`, TS: "1"})
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	if got := store.Snapshot().Sessions["am:A:ns"].Conversation; len(got) != 0 {
		t.Fatalf("conversation = %#v", got)
	}
}

func TestReactionFailureDoesNotFailRCA(t *testing.T) {
	slack := &fakeSlack{reactionErr: errors.New("missing scope")}
	service, _ := startBehaviorService(t,
		activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) { return "answer", nil }),
		slack,
	)
	service.Submit(context.Background(), Event{Channel: "C1", Text: `<!-- alertlens:alertname=A,namespace=ns -->`, TS: "1"})
	waitFor(t, func() bool { return len(slack.replyLog()) == 1 })
	if got := slack.replyLog()[0]; got != "C1:1:answer" {
		t.Fatalf("reply = %q", got)
	}
}

func startBehaviorService(t *testing.T, am Alertmanager, h Holmes, slack *fakeSlack) (*Service, *session.Store) {
	t.Helper()
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	store, err := session.Open(filepath.Join(t.TempDir(), "state.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := New(store, am, h, slack, Config{
		QueueSize: 10, Workers: 1, AlertSessionTTL: 24 * time.Hour,
		AlertPayloadMaxBytes: 32768, RunbookMaxBytes: 8192,
		ConversationMaxBytes: 16384, SlackOutputMaxChars: 2500,
	}, func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return service, store
}

type alertmanagerFunc func(context.Context, string, string) ([]alertmanager.Alert, error)

func (f alertmanagerFunc) Active(ctx context.Context, alertname, namespace string) ([]alertmanager.Alert, error) {
	return f(ctx, alertname, namespace)
}

func activeAlertmanager(alertname, namespace string) alertmanagerFunc {
	return func(context.Context, string, string) ([]alertmanager.Alert, error) {
		return []alertmanager.Alert{{Labels: map[string]string{"alertname": alertname, "namespace": namespace}}}, nil
	}
}

type holmesFunc func(context.Context, holmes.Request) (string, error)

func (f holmesFunc) Chat(ctx context.Context, request holmes.Request) (string, error) {
	return f(ctx, request)
}

type fakeSlack struct {
	mu          sync.Mutex
	reactions   []string
	replies     []string
	reactionErr error
	replyErr    error
}

func (f *fakeSlack) AddReaction(_ context.Context, name, channel, ts string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, "add:"+name+":"+channel+":"+ts)
	return f.reactionErr
}

func (f *fakeSlack) RemoveReaction(_ context.Context, name, channel, ts string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, "remove:"+name+":"+channel+":"+ts)
	return f.reactionErr
}

func (f *fakeSlack) Reply(_ context.Context, channel, threadTS, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, channel+":"+threadTS+":"+text)
	return f.replyErr
}

func (f *fakeSlack) hasReaction(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.reactions {
		if got == want {
			return true
		}
	}
	return false
}

func (f *fakeSlack) reactionLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reactions...)
}

func (f *fakeSlack) replyLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.replies...)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
