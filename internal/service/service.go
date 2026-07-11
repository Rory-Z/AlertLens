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
	"github.com/emqx/alertlens/internal/session"
)

type Event struct {
	ID       string
	Channel  string
	User     string
	BotID    string
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
}

type Config struct {
	QueueSize            int
	Workers              int
	EventDedupTTL        time.Duration
	AlertSessionTTL      time.Duration
	ResolvedSessionTTL   time.Duration
	AlertPayloadMaxBytes int
	RunbookMaxBytes      int
	ConversationMaxBytes int
	SlackOutputMaxChars  int
}

type work struct {
	event    Event
	identity marker.Alert
}

type Service struct {
	store        *session.Store
	alertmanager Alertmanager
	holmes       Holmes
	slack        Slack
	config       Config
	now          func() time.Time
	queue        chan work
	sessionLocks [64]sync.Mutex
	metrics      *observability.Metrics
}

func New(store *session.Store, alertmanager Alertmanager, holmes Holmes, slack Slack, config Config, now func() time.Time, metrics *observability.Metrics) *Service {
	if metrics == nil {
		metrics = observability.New()
	}
	return &Service{
		store: store, alertmanager: alertmanager, holmes: holmes, slack: slack,
		config: config, now: now, queue: make(chan work, config.QueueSize),
		metrics: metrics,
	}
}

func (s *Service) Submit(ctx context.Context, event Event) bool {
	identity, ok := marker.Parse(event.Text)
	if !ok {
		s.metrics.Event("ignored")
		return false
	}
	if event.ID != "" {
		duplicate := false
		now := s.now()
		err := s.store.Update(func(snapshot *session.Snapshot) error {
			if expiresAt, exists := snapshot.EventIDs[event.ID]; exists && expiresAt.After(now) {
				duplicate = true
				return nil
			}
			snapshot.EventIDs[event.ID] = now.Add(s.config.EventDedupTTL)
			return nil
		})
		if err != nil {
			s.metrics.PersistenceError()
			s.metrics.Event("failed")
			return false
		}
		if duplicate {
			s.metrics.Event("duplicate")
			return false
		}
	}
	s.addReaction(ctx, "eyes", event.Channel, event.TS)
	select {
	case s.queue <- work{event: event, identity: identity}:
		s.metrics.Event("accepted")
		s.metrics.QueueDepth(len(s.queue))
		return true
	default:
		s.metrics.Event("dropped")
		s.transition(ctx, event, "eyes", "x")
		return false
	}
}

func (s *Service) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(s.config.Workers)
	for range s.config.Workers {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item := <-s.queue:
					s.metrics.QueueDepth(len(s.queue))
					s.handle(ctx, item)
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			if s.store != nil {
				if err := s.store.Prune(s.now()); err != nil {
					s.metrics.PersistenceError()
				} else {
					s.metrics.Sessions(len(s.store.Snapshot().Sessions))
				}
			}
		}
	}
}

func (s *Service) handle(ctx context.Context, item work) {
	lock := &s.sessionLocks[sessionShard(item.identity.Key())]
	lock.Lock()
	defer lock.Unlock()

	started := time.Now()
	alerts, err := s.alertmanager.Active(ctx, item.identity.Alertname, item.identity.Namespace)
	if err != nil {
		s.metrics.Alertmanager("error", time.Since(started))
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	s.metrics.Alertmanager("success", time.Since(started))
	if len(alerts) == 0 {
		s.resolve(ctx, item)
		return
	}
	if item.identity.Alertname == "Watchdog" {
		s.metrics.Watchdog(s.now())
		s.metrics.Event("watchdog")
		s.transition(ctx, item.event, "eyes", "white_check_mark")
		return
	}

	request, alertContext := buildRequest(item.event, item.identity, alerts, s.config)
	claimed := false
	now := s.now()
	err = s.store.Update(func(snapshot *session.Snapshot) error {
		record, exists := snapshot.Sessions[item.identity.Key()]
		if exists && record.State == "active" {
			record.UpdatedAt = now
			record.ExpiresAt = now.Add(s.config.AlertSessionTTL)
			snapshot.Sessions[item.identity.Key()] = record
			return nil
		}
		claimed = true
		snapshot.Sessions[item.identity.Key()] = session.Record{
			Key: item.identity.Key(), Type: "alert", State: "active",
			Channel: item.event.Channel, ParentTS: item.event.TS, ThreadTS: item.event.TS,
			AlertContext: alertContext, CreatedAt: now, UpdatedAt: now,
			ExpiresAt: now.Add(s.config.AlertSessionTTL),
		}
		return nil
	})
	if err != nil {
		s.metrics.PersistenceError()
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	if !claimed {
		s.metrics.Event("duplicate")
		s.removeReaction(ctx, "eyes", item.event.Channel, item.event.TS)
		return
	}
	s.metrics.Sessions(len(s.store.Snapshot().Sessions))

	s.transition(ctx, item.event, "eyes", "hourglass_flowing_sand")
	s.metrics.HolmesActive(1)
	holmesStarted := time.Now()
	analysis, err := s.holmes.Chat(ctx, request)
	s.metrics.HolmesActive(-1)
	if err != nil {
		s.metrics.Holmes("error", time.Since(holmesStarted))
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "hourglass_flowing_sand", "x")
		return
	}
	s.metrics.Holmes("success", time.Since(holmesStarted))
	analysis = truncateSlack(sanitize(analysis), s.config.SlackOutputMaxChars)
	if err := s.slack.Reply(ctx, item.event.Channel, item.event.TS, analysis); err != nil {
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "hourglass_flowing_sand", "x")
		return
	}
	if err := s.store.Update(func(snapshot *session.Snapshot) error {
		record := snapshot.Sessions[item.identity.Key()]
		record.Conversation = []session.ConversationTurn{{Role: "assistant", Content: analysis}}
		record.UpdatedAt = s.now()
		snapshot.Sessions[item.identity.Key()] = record
		return nil
	}); err != nil {
		s.metrics.PersistenceError()
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "hourglass_flowing_sand", "x")
		return
	}
	s.metrics.Event("firing")
	s.transition(ctx, item.event, "hourglass_flowing_sand", "white_check_mark")
}

func sessionShard(key string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	return hash.Sum32() % 64
}

func (s *Service) resolve(ctx context.Context, item work) {
	record, exists := s.store.Snapshot().Sessions[item.identity.Key()]
	if !exists || record.State != "active" {
		s.metrics.Event("stale")
		s.removeReaction(ctx, "eyes", item.event.Channel, item.event.TS)
		return
	}
	threadTS := record.ThreadTS
	if threadTS == "" {
		threadTS = record.ParentTS
	}
	if err := s.slack.Reply(ctx, record.Channel, threadTS, "🟢 Alertmanager confirms this alert is resolved."); err != nil {
		s.metrics.Event("failed")
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	s.removeReaction(ctx, "eyes", item.event.Channel, item.event.TS)
	s.addReaction(ctx, "large_green_circle", item.event.Channel, item.event.TS)
	s.addReaction(ctx, "large_green_circle", record.Channel, record.ParentTS)
	now := s.now()
	if err := s.store.Update(func(snapshot *session.Snapshot) error {
		record := snapshot.Sessions[item.identity.Key()]
		record.State = "resolved"
		record.UpdatedAt = now
		record.ExpiresAt = now.Add(s.config.ResolvedSessionTTL)
		snapshot.Sessions[item.identity.Key()] = record
		return nil
	}); err != nil {
		s.metrics.PersistenceError()
		s.metrics.Event("failed")
		s.addReaction(ctx, "x", item.event.Channel, item.event.TS)
		return
	}
	s.metrics.Sessions(len(s.store.Snapshot().Sessions))
	s.metrics.Event("resolved")
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
