package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/observability"
)

func TestFiringAlert(t *testing.T) {
	alertmanagerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
          {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-0","alertgroup":"one"},"annotations":{"runbook":"check cpu"}},
          {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-1","alertgroup":"two"},"annotations":{"runbook":"check cpu"}}
        ]`)
	}))
	defer alertmanagerServer.Close()

	requestCh := make(chan holmes.Request, 1)
	holmesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request holmes.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requestCh <- request
		_, _ = io.WriteString(w, `{"analysis":"CPU saturation on api pods"}`)
	}))
	defer holmesServer.Close()

	slack := &fakeSlack{}
	service := startService(t,
		alertmanager.New(testURL(t, alertmanagerServer.URL), time.Second),
		holmes.New(testURL(t, holmesServer.URL), time.Second), slack, Config{HolmesResponseLanguage: "zh-CN"})
	event := Event{Channel: "C1", Text: "FIRING HighCPU\n<!-- alertlens:alertname=HighCPU,namespace=prod,status=firing -->", TS: "100.1"}
	if !service.Submit(context.Background(), event) {
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
	if got := slack.reactionLog(); !slices.Equal(got, wantReactions) {
		t.Fatalf("reactions = %#v, want %#v", got, wantReactions)
	}
	request := <-requestCh
	if request.RequestSource != "alert_investigation" || request.SourceRef != "am:HighCPU:prod" ||
		request.ConversationID != "slack:C1:100.1" ||
		!strings.Contains(request.AdditionalSystemPrompt, "Respond in zh-CN.") ||
		!strings.Contains(request.AdditionalSystemPrompt, "AlertLens verified") ||
		!strings.Contains(request.Ask, `"verified":true`) ||
		!strings.Contains(request.Ask, `"alertgroup":"one"`) || !strings.Contains(request.Ask, `"alertgroup":"two"`) {
		t.Fatalf("Holmes request = %#v", request)
	}
	if got := slack.replyLog(); len(got) != 1 || got[0] != "C1:100.1:CPU saturation on api pods" {
		t.Fatalf("replies = %#v", got)
	}
}

func TestEveryFiringNotificationRunsRCA(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	service := startService(t, activeAlertmanager("A", "ns"), holmesFunc(func(_ context.Context, request holmes.Request) (string, error) {
		if request.AdditionalSystemPrompt != investigationSystemPrompt+verifiedAlertPrompt {
			t.Errorf("system prompt = %q", request.AdditionalSystemPrompt)
		}
		calls.Add(1)
		return "answer", nil
	}), slack, Config{HolmesResponseLanguage: "auto"})
	for _, ts := range []string{"1", "2"} {
		if !service.Submit(context.Background(), Event{Channel: "C1", TS: ts,
			Text: `<!-- alertlens:alertname=A,namespace=ns,status=firing -->`}) {
			t.Fatalf("firing %s rejected", ts)
		}
	}
	waitFor(t, func() bool { return calls.Load() == 2 })
	if len(slack.replyLog()) != 2 {
		t.Fatalf("replies = %#v", slack.replyLog())
	}
}

func TestAlertmanagerFailureStopsBeforeHolmes(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	service := startService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			return nil, errors.New("dial tcp: connection refused")
		}),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			calls.Add(1)
			return "answer", nil
		}), slack, Config{})
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	replies := slack.replyLog()
	if calls.Load() != 0 || len(replies) != 1 || !strings.Contains(replies[0], "dial tcp: connection refused") ||
		strings.Contains(replies[0], "timeout") {
		t.Fatalf("calls = %d, replies = %#v", calls.Load(), replies)
	}
}

func TestNoMatchingActiveAlertStopsBeforeHolmes(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	metrics := observability.New()
	service := New(
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			return nil, nil
		}),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			calls.Add(1)
			return "answer", nil
		}), slack, Config{
			QueueSize: 10, Workers: 1, AlertPayloadMaxBytes: 32768,
			RunbookMaxBytes: 8192, ConversationMaxBytes: 256 << 10, SlackOutputMaxChars: 2500,
		}, metrics)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	if replies := slack.replyLog(); calls.Load() != 0 || !slices.Equal(replies, []string{
		"C1:1:" + AlertmanagerFailureReplyPrefix + " no active alert matches Alert Identity",
	}) {
		t.Fatalf("calls = %d, replies = %#v", calls.Load(), replies)
	}
	w := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), `alertlens_alertmanager_requests_total{outcome="no_match"} 1`) {
		t.Fatalf("metrics = %q", w.Body.String())
	}
}

func TestVerifiedSnapshotTooLargeStopsBeforeHolmes(t *testing.T) {
	alertname := strings.Repeat("A", 100)
	var calls atomic.Int32
	slack := &fakeSlack{}
	service := startService(t, activeAlertmanager(alertname, "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			calls.Add(1)
			return "answer", nil
		}), slack, Config{AlertPayloadMaxBytes: 128})
	service.Submit(context.Background(), firingEvent("1", alertname, "ns"))
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	if replies := slack.replyLog(); calls.Load() != 0 || len(replies) != 1 ||
		!strings.Contains(replies[0], "verified alert snapshot exceeds 128 bytes") {
		t.Fatalf("calls = %d, replies = %#v", calls.Load(), replies)
	}
}

func TestRCAHolmesFailureRepliesSanitizedReason(t *testing.T) {
	slack := &fakeSlack{}
	service := startService(t, activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			return "", errors.New("upstream rejected password: super-secret")
		}), slack, Config{})
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
	replies := slack.replyLog()
	if len(replies) != 1 || !strings.Contains(replies[0], "password=[REDACTED]") || strings.Contains(replies[0], "super-secret") {
		t.Fatalf("replies = %#v", replies)
	}
}

func TestWatchdogRunsOrdinaryRCA(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{}
	service := startService(t, activeAlertmanager("Watchdog", ""), holmesFunc(func(context.Context, holmes.Request) (string, error) {
		calls.Add(1)
		return "answer", nil
	}), slack, Config{})
	service.Submit(context.Background(), firingEvent("1", "Watchdog", ""))
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:1") })
	if calls.Load() != 1 || len(slack.replyLog()) != 1 {
		t.Fatalf("calls = %d, replies = %#v", calls.Load(), slack.replyLog())
	}
}

func TestResolvedNotificationOnlyMarksCurrentMessage(t *testing.T) {
	slack := &fakeSlack{}
	service := startService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			t.Fatal("Alertmanager was called")
			return nil, nil
		}),
		holmesFunc(func(context.Context, holmes.Request) (string, error) {
			t.Fatal("Holmes was called")
			return "", nil
		}), slack, Config{})
	service.Submit(context.Background(), Event{Channel: "C1", TS: "2", Text: `<!-- alertlens:alertname=A,namespace=ns,status=resolved -->`})
	waitFor(t, func() bool { return slack.hasReaction("add:large_green_circle:C1:2") })
	want := []string{"add:eyes:C1:2", "remove:eyes:C1:2", "add:large_green_circle:C1:2"}
	if !slices.Equal(slack.reactionLog(), want) || len(slack.replyLog()) != 0 {
		t.Fatalf("reactions = %#v, replies = %#v", slack.reactionLog(), slack.replyLog())
	}
}

func TestSubmitRejectsInvalidMarkerAndIgnoresUnmarkedMessage(t *testing.T) {
	slack := &fakeSlack{}
	service := newService(nil, nil, slack, Config{QueueSize: 2, Workers: 1})
	if service.Submit(context.Background(), Event{Channel: "C1", TS: "1", Text: "ordinary"}) {
		t.Fatal("ordinary message accepted")
	}
	if service.Submit(context.Background(), Event{Channel: "C1", TS: "2", Text: `<!-- alertlens:alertname=A,namespace=ns -->`}) {
		t.Fatal("invalid marker accepted")
	}
	if got := slack.reactionLog(); !slices.Equal(got, []string{"add:x:C1:2"}) {
		t.Fatalf("reactions = %#v", got)
	}
}

func TestAskReconstructsAndBoundsSlackConversationWithoutAlertmanager(t *testing.T) {
	requestCh := make(chan holmes.Request, 1)
	slack := &fakeSlack{conversation: []ConversationMessage{
		{Role: "user", Content: "root"},
		{Role: "user", Content: "01"},
		{Role: "assistant", Content: "23"},
		{Role: "user", Content: "45"},
		{Role: "assistant", Content: "67"},
	}}
	service := startService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			t.Fatal("Alertmanager must not be called for Ask")
			return nil, nil
		}),
		holmesFunc(func(_ context.Context, request holmes.Request) (string, error) {
			requestCh <- request
			return "answer", nil
		}), slack, Config{ConversationMaxBytes: 10, HolmesResponseLanguage: "zh-CN"})
	event := Event{Channel: "C1", ThreadTS: "1", TS: "6", Text: "reply in English", Mention: true}
	if !service.Submit(context.Background(), event) {
		t.Fatal("Ask was rejected")
	}
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:6") })
	request := <-requestCh
	wantPrompt := investigationSystemPrompt + " Respond in zh-CN."
	wantHistory := []holmes.Message{
		{Role: "system", Content: wantPrompt},
		{Role: "user", Content: "root"},
		{Role: "assistant", Content: "23"},
		{Role: "user", Content: "45"},
		{Role: "assistant", Content: "67"},
	}
	if !slices.Equal(request.ConversationHistory, wantHistory) || request.RequestSource != "freeform" ||
		request.SourceRef != "slack:C1:1" || request.ConversationID != "slack:C1:1" ||
		request.AdditionalSystemPrompt != wantPrompt || !strings.Contains(request.Ask, "reply in E") ||
		len(slack.conversationLog()) != 1 {
		t.Fatalf("request = %#v, conversation calls = %#v", request, slack.conversationLog())
	}
}

func TestAskSlackHistoryFailureStopsBeforeHolmes(t *testing.T) {
	var calls atomic.Int32
	slack := &fakeSlack{conversationErr: errors.New("missing_scope")}
	service := startService(t, nil, holmesFunc(func(context.Context, holmes.Request) (string, error) {
		calls.Add(1)
		return "answer", nil
	}), slack, Config{})
	service.Submit(context.Background(), Event{Channel: "C1", ThreadTS: "1", TS: "2", Text: "why?", Mention: true})
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:2") })
	if calls.Load() != 0 || len(slack.replyLog()) != 0 {
		t.Fatalf("Holmes calls = %d, replies = %#v", calls.Load(), slack.replyLog())
	}
}

func TestAskHolmesFailureRepliesActualReason(t *testing.T) {
	slack := &fakeSlack{conversation: []ConversationMessage{{Role: "user", Content: "root"}}}
	service := startService(t, nil, holmesFunc(func(context.Context, holmes.Request) (string, error) {
		return "", errors.New("upstream reset by peer")
	}), slack, Config{})
	service.Submit(context.Background(), Event{Channel: "C1", ThreadTS: "1", TS: "2", Text: "why?", Mention: true})
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:2") })
	replies := slack.replyLog()
	if len(replies) != 1 || !strings.Contains(replies[0], "upstream reset by peer") || strings.Contains(replies[0], "timeout") {
		t.Fatalf("replies = %#v", replies)
	}
}

func TestTopLevelAskDoesNotReadThreadOrAlertmanager(t *testing.T) {
	requestCh := make(chan holmes.Request, 1)
	slack := &fakeSlack{}
	service := startService(t, nil, holmesFunc(func(_ context.Context, request holmes.Request) (string, error) {
		requestCh <- request
		return "answer", nil
	}), slack, Config{})
	service.Submit(context.Background(), Event{Channel: "C1", TS: "10", Text: "what is wrong?", Mention: true})
	waitFor(t, func() bool { return slack.hasReaction("add:white_check_mark:C1:10") })
	request := <-requestCh
	if len(request.ConversationHistory) != 0 || request.SourceRef != "slack:C1:10" || len(slack.conversationLog()) != 0 {
		t.Fatalf("request = %#v, conversations = %#v", request, slack.conversationLog())
	}
}

func TestExplicitMentionWinsOverPastedAlertMarker(t *testing.T) {
	requestCh := make(chan holmes.Request, 1)
	var alertmanagerCalls atomic.Int32
	service := startService(t,
		alertmanagerFunc(func(context.Context, string, string) ([]alertmanager.Alert, error) {
			alertmanagerCalls.Add(1)
			return nil, nil
		}),
		holmesFunc(func(_ context.Context, request holmes.Request) (string, error) {
			requestCh <- request
			return "answer", nil
		}), &fakeSlack{}, Config{})
	service.Submit(context.Background(), Event{Channel: "C1", TS: "10", Mention: true,
		Text: "what does this mean?\n<!-- alertlens:alertname=A,namespace=ns,status=firing -->"})
	request := <-requestCh
	if request.RequestSource != "freeform" || alertmanagerCalls.Load() != 0 {
		t.Fatalf("request source = %q, Alertmanager calls = %d", request.RequestSource, alertmanagerCalls.Load())
	}
}

func TestQueueSaturationRejectsWithFailureReaction(t *testing.T) {
	slack := &fakeSlack{}
	service := newService(nil, nil, slack, Config{QueueSize: 1, Workers: 1})
	if !service.Submit(context.Background(), firingEvent("1", "A", "ns")) {
		t.Fatal("first event rejected")
	}
	if service.Submit(context.Background(), firingEvent("2", "B", "ns")) {
		t.Fatal("second event accepted")
	}
	if !slack.hasReaction("add:x:C1:2") {
		t.Fatalf("reactions = %#v", slack.reactionLog())
	}
}

func TestRunDrainsAcceptedWorkBeforeReturning(t *testing.T) {
	release := make(chan struct{})
	slack := &fakeSlack{}
	service := newService(activeAlertmanager("A", "ns"), holmesFunc(func(context.Context, holmes.Request) (string, error) {
		<-release
		return "answer", nil
	}), slack, Config{QueueSize: 1, Workers: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned before accepted work completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not drain")
	}
	if !slack.hasReaction("add:white_check_mark:C1:1") {
		t.Fatalf("reactions = %#v", slack.reactionLog())
	}
}

func TestSubmitAfterRunStopsIsRejected(t *testing.T) {
	service := newService(nil, nil, &fakeSlack{}, Config{QueueSize: 1, Workers: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	cancel()
	<-done
	if service.Submit(context.Background(), firingEvent("1", "A", "ns")) {
		t.Fatal("event accepted after stop")
	}
}

func TestReplyFailureEndsWithFailureReaction(t *testing.T) {
	slack := &fakeSlack{replyErr: errors.New("Slack unavailable")}
	service := startService(t, activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) { return "answer", nil }), slack, Config{})
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	waitFor(t, func() bool { return slack.hasReaction("add:x:C1:1") })
}

func TestReactionFailureDoesNotFailRCA(t *testing.T) {
	slack := &fakeSlack{reactionErr: errors.New("reaction denied")}
	service := startService(t, activeAlertmanager("A", "ns"),
		holmesFunc(func(context.Context, holmes.Request) (string, error) { return "answer", nil }), slack, Config{})
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	waitFor(t, func() bool { return len(slack.replyLog()) == 1 })
}

func TestSameSlackThreadIsSerialized(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	service := startService(t, activeAlertmanager("A", "ns"), holmesFunc(func(context.Context, holmes.Request) (string, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return "answer", nil
	}), &fakeSlack{}, Config{Workers: 2})
	service.Submit(context.Background(), firingEvent("1", "A", "ns"))
	<-firstStarted
	service.Submit(context.Background(), Event{Channel: "C1", ThreadTS: "1", TS: "2", Text: "why?", Mention: true})
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("Holmes calls before release = %d", calls.Load())
	}
	close(releaseFirst)
	waitFor(t, func() bool { return calls.Load() == 2 })
}

func newService(am Alertmanager, h Holmes, slack Slack, cfg Config) *Service {
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 10
	}
	if cfg.Workers == 0 {
		cfg.Workers = 1
	}
	if cfg.AlertPayloadMaxBytes == 0 {
		cfg.AlertPayloadMaxBytes = 32768
	}
	if cfg.RunbookMaxBytes == 0 {
		cfg.RunbookMaxBytes = 8192
	}
	if cfg.ConversationMaxBytes == 0 {
		cfg.ConversationMaxBytes = 256 << 10
	}
	if cfg.SlackOutputMaxChars == 0 {
		cfg.SlackOutputMaxChars = 2500
	}
	return New(am, h, slack, cfg, nil)
}

func startService(t *testing.T, am Alertmanager, h Holmes, slack Slack, cfg Config) *Service {
	t.Helper()
	service := newService(am, h, slack, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return service
}

func firingEvent(ts, alertname, namespace string) Event {
	return Event{Channel: "C1", TS: ts, Text: "<!-- alertlens:alertname=" + alertname +
		",namespace=" + namespace + ",status=firing -->"}
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
	mu                sync.Mutex
	reactions         []string
	replies           []string
	conversation      []ConversationMessage
	conversationCalls []string
	conversationErr   error
	reactionErr       error
	replyErr          error
}

func (f *fakeSlack) Conversation(_ context.Context, channel, threadTS, currentTS string) ([]ConversationMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversationCalls = append(f.conversationCalls, channel+":"+threadTS+":"+currentTS)
	return append([]ConversationMessage(nil), f.conversation...), f.conversationErr
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
	return slices.Contains(f.reactions, want)
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

func (f *fakeSlack) conversationLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.conversationCalls...)
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

func testURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
