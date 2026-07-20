package service

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/marker"
	"github.com/emqx/alertlens/internal/observability"
)

const (
	defaultDrainTimeout                    = 25 * time.Second
	defaultShutdownReplyTimeout            = 5 * time.Second
	AlertmanagerFailureReplyPrefix         = "⚠️ Alertmanager verification failed:"
	HolmesFailureReplyPrefix               = "⚠️ Holmes request failed:"
	HolmesAnswerDeliveryFailureReplyPrefix = "⚠️ Holmes answer delivery failed:"
	ScheduledFailureReplyPrefix            = "⚠️ Scheduled investigation failed:"
	ShutdownReply                          = "AlertLens shutting down"
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
	Post(context.Context, string, string) (string, error)
	Reply(context.Context, string, string, string) error
	Conversation(context.Context, string, string, string) ([]ConversationMessage, error)
}

type ScheduledInvestigation struct {
	Name     string
	Schedule string
	Prompt   string
}

type Config struct {
	QueueSize               int
	Workers                 int
	AlertPayloadMaxBytes    int
	RunbookMaxBytes         int
	ConversationMaxBytes    int
	HolmesResponseLanguage  string
	MonitoredChannel        string
	ScheduledInvestigations []ScheduledInvestigation
}

type work struct {
	event                  Event
	identity               marker.Alert
	scheduledInvestigation *ScheduledInvestigation
}

type replyContext struct {
	context.Context
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

func (s *Service) SubmitScheduled(ctx context.Context, investigation ScheduledInvestigation) bool {
	rootTS, err := s.slack.Post(ctx, s.config.MonitoredChannel, "Scheduled investigation started: "+investigation.Name)
	if err != nil || rootTS == "" {
		s.metrics.ScheduledInvestigation("failed")
		slog.Error("scheduled investigation failed", "name", investigation.Name, "schedule", investigation.Schedule)
		return false
	}
	event := Event{Channel: s.config.MonitoredChannel, TS: rootTS}
	s.intakeMu.RLock()
	if !s.accepting {
		s.intakeMu.RUnlock()
		s.failScheduledIntake(ctx, event, investigation, "AlertLens is shutting down")
		return false
	}
	select {
	case s.queue <- work{event: event, scheduledInvestigation: &investigation}:
		s.intakeMu.RUnlock()
		s.metrics.QueueDepth(len(s.queue))
		return true
	default:
		s.intakeMu.RUnlock()
		s.failScheduledIntake(ctx, event, investigation, "work queue is full")
		return false
	}
}

func (s *Service) failScheduledIntake(
	ctx context.Context, event Event, investigation ScheduledInvestigation, reason string,
) {
	_ = s.slack.Reply(ctx, event.Channel, event.TS, truncateSlack(
		ScheduledFailureReplyPrefix+" "+reason))
	s.transition(ctx, event, "", "x")
	s.metrics.ScheduledInvestigation("failed")
	slog.Error("scheduled investigation failed", "name", investigation.Name, "schedule", investigation.Schedule)
}

func (s *Service) Run(ctx context.Context) {
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	var forcedReplyContext atomic.Pointer[replyContext]
	forcedReplyContext.Store(&replyContext{Context: context.Background()})
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	scheduler := cron.New(cron.WithLocation(time.UTC), cron.WithParser(parser))
	for _, investigation := range s.config.ScheduledInvestigations {
		investigation := investigation
		if _, err := scheduler.AddFunc(investigation.Schedule, func() {
			s.SubmitScheduled(workCtx, investigation)
		}); err != nil {
			slog.Error("scheduled investigation configuration failed",
				"name", investigation.Name, "schedule", investigation.Schedule)
		}
	}
	scheduler.Start()
	var workers sync.WaitGroup
	workers.Add(s.config.Workers)
	for range s.config.Workers {
		go func() {
			defer workers.Done()
			for item := range s.queue {
				if workCtx.Err() != nil {
					s.failScheduledShutdown(forcedReplyContext.Load().Context, item)
					continue
				}
				s.metrics.QueueDepth(len(s.queue))
				s.handle(workCtx, item)
			}
		}()
	}

	<-ctx.Done()
	schedulerDone := scheduler.Stop()

	done := make(chan struct{})
	go func() {
		<-schedulerDone.Done()
		s.intakeMu.Lock()
		if s.accepting {
			s.accepting = false
			close(s.queue)
		}
		s.intakeMu.Unlock()
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(s.drainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		replyCtx, cancelReplies := context.WithTimeout(context.Background(), defaultShutdownReplyTimeout)
		forcedReplyContext.Store(&replyContext{Context: replyCtx})
		cancelWork()
		<-done
		cancelReplies()
	}
}

func (s *Service) failScheduledShutdown(ctx context.Context, item work) {
	if item.scheduledInvestigation == nil {
		return
	}
	_ = s.slack.Reply(ctx, item.event.Channel, item.event.TS,
		truncateSlack(ShutdownReply))
	s.transition(ctx, item.event, "", "x")
	s.metrics.ScheduledInvestigation("failed")
	slog.Error("scheduled investigation failed",
		"name", item.scheduledInvestigation.Name, "schedule", item.scheduledInvestigation.Schedule)
}

func (s *Service) handle(ctx context.Context, item work) {
	if item.scheduledInvestigation != nil {
		s.handleScheduled(ctx, item.event, *item.scheduledInvestigation)
		return
	}
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

func (s *Service) handleScheduled(ctx context.Context, event Event, investigation ScheduledInvestigation) {
	unlock := s.lockThread(event.Channel, event.TS)
	defer unlock()
	request := holmes.Request{
		Ask:                    investigation.Prompt,
		AdditionalSystemPrompt: scheduledHolmesSystemPrompt(s.config.HolmesResponseLanguage),
		RequestSource:          "scheduled_investigation",
		SourceRef:              "schedule:" + investigation.Name,
		ConversationID:         threadLockKey(event.Channel, event.TS),
	}
	outcome := "failed"
	if s.runHolmesFrom(ctx, event, event.TS, request, "") {
		outcome = "success"
	}
	s.metrics.ScheduledInvestigation(outcome)
	if outcome == "success" {
		slog.Info("scheduled investigation completed", "name", investigation.Name, "schedule", investigation.Schedule)
	} else {
		slog.Error("scheduled investigation failed", "name", investigation.Name, "schedule", investigation.Schedule)
	}
}

func (s *Service) failVerification(ctx context.Context, event Event, reason string) {
	_ = s.slack.Reply(ctx, event.Channel, event.TS, truncateSlack(
		AlertmanagerFailureReplyPrefix+" "+reason))
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
	return s.runHolmesFrom(ctx, event, replyThreadTS, request, "eyes")
}

func (s *Service) runHolmesFrom(
	ctx context.Context, event Event, replyThreadTS string, request holmes.Request, currentReaction string,
) bool {
	s.transition(ctx, event, currentReaction, "hourglass_flowing_sand")
	s.metrics.HolmesActive(1)
	started := time.Now()
	answer, err := s.holmes.Chat(ctx, request)
	s.metrics.HolmesActive(-1)
	analysisTooLarge := errors.Is(err, holmes.ErrAnalysisTooLarge)
	requestFailed := err != nil && (errors.Is(err, holmes.ErrInvalidResponse) || !analysisTooLarge)
	if requestFailed {
		s.metrics.Holmes("error", time.Since(started))
	} else {
		s.metrics.Holmes("success", time.Since(started))
	}
	if requestFailed && answer == "" {
		return s.failHolmesRequest(ctx, event, replyThreadTS, err)
	}
	answer = sanitize(answer)
	var parts []string
	if analysisTooLarge {
		parts = splitSlackOverflow(answer)
	} else {
		parts = splitSlack(answer)
	}
	for index, part := range parts[:min(len(parts), slackAnswerMaxParts)] {
		if err := s.slack.Reply(ctx, event.Channel, replyThreadTS, part); err != nil {
			return s.failHolmesAnswerDelivery(ctx, event, replyThreadTS,
				fmt.Sprintf("The answer is incomplete; ask AlertLens again to retry. part %d of %d: %s",
					index+1, len(parts), sanitize(err.Error())))
		}
	}
	if requestFailed {
		return s.failHolmesRequest(ctx, event, replyThreadTS, err)
	}
	if analysisTooLarge {
		return s.failHolmesAnswerDelivery(ctx, event, replyThreadTS,
			fmt.Sprintf("The answer is incomplete; %s; only the first %d parts were delivered. "+
				"Ask AlertLens a narrower follow-up question.", err, slackAnswerMaxParts))
	}
	if len(parts) > slackAnswerMaxParts {
		return s.failHolmesAnswerDelivery(ctx, event, replyThreadTS,
			fmt.Sprintf("The answer is incomplete; answer has %d parts; only the first %d were delivered. "+
				"Ask AlertLens a narrower follow-up question.", len(parts), slackAnswerMaxParts))
	}
	s.transition(ctx, event, "hourglass_flowing_sand", "white_check_mark")
	return true
}

func (s *Service) failHolmesRequest(
	ctx context.Context, event Event, replyThreadTS string, err error,
) bool {
	s.metrics.Event("failed")
	slog.Error("Holmes request failed", "reason", sanitize(err.Error()))
	replyCtx := ctx
	message := HolmesFailureReplyPrefix + " " + sanitize(err.Error())
	cancelReply := func() {}
	if ctx.Err() != nil {
		replyCtx, cancelReply = context.WithTimeout(context.Background(), defaultShutdownReplyTimeout)
		message = ShutdownReply
	}
	_ = s.slack.Reply(replyCtx, event.Channel, replyThreadTS, truncateSlack(message))
	s.transition(replyCtx, event, "hourglass_flowing_sand", "x")
	cancelReply()
	return false
}

func (s *Service) failHolmesAnswerDelivery(
	ctx context.Context, event Event, replyThreadTS, reason string,
) bool {
	s.metrics.Event("failed")
	slog.Error("Holmes answer delivery failed", "reason", sanitize(reason))
	_ = s.slack.Reply(ctx, event.Channel, replyThreadTS,
		truncateSlack(HolmesAnswerDeliveryFailureReplyPrefix+" "+reason))
	s.transition(ctx, event, "hourglass_flowing_sand", "x")
	return false
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
