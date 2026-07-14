package service

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/marker"
	"github.com/emqx/alertlens/internal/observability"
)

const (
	defaultDrainTimeout            = 25 * time.Second
	AlertmanagerFailureReplyPrefix = "⚠️ Alertmanager verification failed:"
	HolmesFailureReplyPrefix       = "⚠️ Holmes request failed:"
)

type Event struct {
	Channel  string
	Text     string
	TS       string
	ThreadTS string
	Mention  bool
}

type Alertmanager interface {
	Active(context.Context, string, string) ([]alertmanager.Alert, error)
}

type Holmes interface {
	Chat(context.Context, holmes.Request) (string, error)
}

type Slack interface {
	AddReaction(context.Context, string, string, string) error
	RemoveReaction(context.Context, string, string, string) error
	Reply(context.Context, string, string, string) error
	Conversation(context.Context, string, string, string) ([]ConversationMessage, error)
}

type Config struct {
	QueueSize              int
	Workers                int
	AlertPayloadMaxBytes   int
	RunbookMaxBytes        int
	ConversationMaxBytes   int
	SlackOutputMaxChars    int
	HolmesResponseLanguage string
}

type work struct {
	event    Event
	identity marker.Alert
}

type Service struct {
	alertmanager Alertmanager
	holmes       Holmes
	slack        Slack
	config       Config
	drainTimeout time.Duration
	queue        chan work
	intakeMu     sync.RWMutex
	accepting    bool
	threadLocks  [64]sync.Mutex
	metrics      *observability.Metrics
}

func New(alertmanager Alertmanager, holmes Holmes, slack Slack, config Config, metrics *observability.Metrics) *Service {
	if metrics == nil {
		metrics = observability.New()
	}
	return &Service{
		alertmanager: alertmanager,
		holmes:       holmes,
		slack:        slack,
		config:       config,
		drainTimeout: defaultDrainTimeout,
		queue:        make(chan work, config.QueueSize),
		metrics:      metrics,
		accepting:    true,
	}
}

func (s *Service) Submit(ctx context.Context, event Event) bool {
	identity, validMarker := marker.Parse(event.Text)
	if !event.Mention && !validMarker {
		if marker.Present(event.Text) {
			s.metrics.Event("failed")
			s.addReaction(ctx, "x", event.Channel, event.TS)
			return false
		}
		s.metrics.Event("ignored")
		return false
	}

	s.intakeMu.RLock()
	if !s.accepting {
		s.intakeMu.RUnlock()
		s.metrics.Event("dropped")
		return false
	}
	s.addReaction(ctx, "eyes", event.Channel, event.TS)
	select {
	case s.queue <- work{event: event, identity: identity}:
		s.intakeMu.RUnlock()
		s.metrics.Event("accepted")
		s.metrics.QueueDepth(len(s.queue))
		return true
	default:
		s.intakeMu.RUnlock()
		s.metrics.Event("dropped")
		s.transition(ctx, event, "eyes", "x")
		return false
	}
}

func (s *Service) Run(ctx context.Context) {
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	var workers sync.WaitGroup
	workers.Add(s.config.Workers)
	for range s.config.Workers {
		go func() {
			defer workers.Done()
			for item := range s.queue {
				if workCtx.Err() != nil {
					return
				}
				s.metrics.QueueDepth(len(s.queue))
				s.handle(workCtx, item)
			}
		}()
	}

	<-ctx.Done()
	s.intakeMu.Lock()
	if s.accepting {
		s.accepting = false
		close(s.queue)
	}
	s.intakeMu.Unlock()

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(s.drainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancelWork()
		<-done
	}
}

func (s *Service) handle(ctx context.Context, item work) {
	if item.event.Mention {
		s.handleAsk(ctx, item.event)
		return
	}
	if item.identity.Status == "resolved" {
		s.transition(ctx, item.event, "eyes", "large_green_circle")
		s.metrics.Event("resolved")
		return
	}

	unlock := s.lockThread(item.event.Channel, item.event.TS)
	defer unlock()

	started := time.Now()
	alerts, err := s.alertmanager.Active(ctx, item.identity.Alertname, item.identity.Namespace)
	if err != nil {
		s.metrics.Alertmanager("error", time.Since(started))
		s.failVerification(ctx, item.event, sanitize(err.Error()))
		return
	}
	if len(alerts) == 0 {
		s.metrics.Alertmanager("no_match", time.Since(started))
		s.failVerification(ctx, item.event, "no active alert matches Alert Identity")
		return
	}
	s.metrics.Alertmanager("success", time.Since(started))

	request, err := buildRequest(item.event, item.identity, alerts, s.config)
	if err != nil {
		s.failVerification(ctx, item.event, sanitize(err.Error()))
		return
	}
	if !s.runHolmes(ctx, item.event, item.event.TS, request) {
		return
	}
	s.metrics.Event("firing")
}

func (s *Service) failVerification(ctx context.Context, event Event, reason string) {
	_ = s.slack.Reply(ctx, event.Channel, event.TS, truncateSlack(
		AlertmanagerFailureReplyPrefix+" "+reason, s.config.SlackOutputMaxChars))
	s.metrics.Event("failed")
	s.transition(ctx, event, "eyes", "x")
}

func (s *Service) handleAsk(ctx context.Context, event Event) {
	parentTS := event.ThreadTS
	if parentTS == "" {
		parentTS = event.TS
	}
	unlock := s.lockThread(event.Channel, parentTS)
	defer unlock()

	var messages []ConversationMessage
	if event.ThreadTS != "" {
		var err error
		messages, err = s.slack.Conversation(ctx, event.Channel, parentTS, event.TS)
		if err != nil {
			s.metrics.Event("failed")
			s.transition(ctx, event, "eyes", "x")
			return
		}
		messages = boundConversation(messages, s.config.ConversationMaxBytes)
	}

	question := truncateBytes(sanitize(event.Text), s.config.ConversationMaxBytes)
	key := threadLockKey(event.Channel, parentTS)
	prompt := holmesSystemPrompt(s.config.HolmesResponseLanguage)
	request := holmes.Request{
		Ask:                    "<untrusted_user_question>\n" + jsonString(question) + "\n</untrusted_user_question>",
		ConversationHistory:    conversationHistory(messages, prompt),
		AdditionalSystemPrompt: prompt,
		RequestSource:          "freeform",
		SourceRef:              key,
		ConversationID:         key,
	}

	if !s.runHolmes(ctx, event, parentTS, request) {
		return
	}
	s.metrics.Event("freeform")
}

func (s *Service) runHolmes(ctx context.Context, event Event, replyThreadTS string, request holmes.Request) bool {
	s.transition(ctx, event, "eyes", "hourglass_flowing_sand")
	s.metrics.HolmesActive(1)
	started := time.Now()
	answer, err := s.holmes.Chat(ctx, request)
	s.metrics.HolmesActive(-1)
	if err != nil {
		s.metrics.Holmes("error", time.Since(started))
		s.metrics.Event("failed")
		_ = s.slack.Reply(ctx, event.Channel, replyThreadTS,
			truncateSlack(HolmesFailureReplyPrefix+" "+sanitize(err.Error()), s.config.SlackOutputMaxChars))
		s.transition(ctx, event, "hourglass_flowing_sand", "x")
		return false
	}
	s.metrics.Holmes("success", time.Since(started))
	answer = truncateSlack(sanitize(answer), s.config.SlackOutputMaxChars)
	if err := s.slack.Reply(ctx, event.Channel, replyThreadTS, answer); err != nil {
		s.metrics.Event("failed")
		s.transition(ctx, event, "hourglass_flowing_sand", "x")
		return false
	}
	s.transition(ctx, event, "hourglass_flowing_sand", "white_check_mark")
	return true
}

func (s *Service) lockThread(channel, parentTS string) func() {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(threadLockKey(channel, parentTS)))
	lock := &s.threadLocks[hash.Sum32()%uint32(len(s.threadLocks))]
	lock.Lock()
	return lock.Unlock
}

func threadLockKey(channel, parentTS string) string {
	return "slack:" + channel + ":" + parentTS
}

func (s *Service) transition(ctx context.Context, event Event, remove, add string) {
	if remove != "" {
		s.removeReaction(ctx, remove, event.Channel, event.TS)
	}
	if add != "" {
		s.addReaction(ctx, add, event.Channel, event.TS)
	}
}

func (s *Service) addReaction(ctx context.Context, name, channel, ts string) {
	outcome := "success"
	if err := s.slack.AddReaction(ctx, name, channel, ts); err != nil {
		outcome = "error"
	}
	s.metrics.Reaction("add", outcome)
}

func (s *Service) removeReaction(ctx context.Context, name, channel, ts string) {
	outcome := "success"
	if err := s.slack.RemoveReaction(ctx, name, channel, ts); err != nil {
		outcome = "error"
	}
	s.metrics.Reaction("remove", outcome)
}
