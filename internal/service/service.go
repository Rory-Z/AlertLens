package service

import (
	"context"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/marker"
	"github.com/emqx/alertlens/internal/observability"
	"github.com/emqx/alertlens/internal/scheduled"
	"github.com/emqx/alertlens/internal/slackmsg"
)

const (
	defaultDrainTimeout         = 25 * time.Second
	defaultShutdownReplyTimeout = 5 * time.Second
)

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
	Conversation(context.Context, string, string, string) ([]slackmsg.Message, error)
}

type Config struct {
	QueueSize               int
	Workers                 int
	AlertPayloadMaxBytes    int
	RunbookMaxBytes         int
	ConversationMaxBytes    int
	HolmesResponseLanguage  string
	MonitoredChannel        string
	ScheduledInvestigations []scheduled.Investigation
}

type work struct {
	event                  slackmsg.Event
	identity               marker.Alert
	scheduledInvestigation *scheduled.Investigation
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

func (s *Service) Submit(ctx context.Context, event slackmsg.Event) bool {
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
	var forcedReplyContext atomic.Pointer[replyContext]
	forcedReplyContext.Store(&replyContext{Context: context.Background()})
	scheduler := cron.New(cron.WithLocation(time.UTC))
	for _, investigation := range s.config.ScheduledInvestigations {
		scheduler.Schedule(investigation, cron.FuncJob(func() {
			s.SubmitScheduled(workCtx, investigation)
		}))
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

	s.handleFiring(ctx, item)
}

func (s *Service) lockThread(channel, parentTS string) func() {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(slackmsg.ConversationID(channel, parentTS)))
	lock := &s.threadLocks[hash.Sum32()%uint32(len(s.threadLocks))]
	lock.Lock()
	return lock.Unlock
}
