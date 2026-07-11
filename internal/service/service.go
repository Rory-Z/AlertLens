package service

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/marker"
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
}

func New(store *session.Store, alertmanager Alertmanager, holmes Holmes, slack Slack, config Config, now func() time.Time) *Service {
	return &Service{
		store: store, alertmanager: alertmanager, holmes: holmes, slack: slack,
		config: config, now: now, queue: make(chan work, config.QueueSize),
	}
}

func (s *Service) Submit(ctx context.Context, event Event) bool {
	identity, ok := marker.Parse(event.Text)
	if !ok {
		return false
	}
	if event.ID != "" {
		duplicate := false
		now := s.now()
		if err := s.store.Update(func(snapshot *session.Snapshot) error {
			if expiresAt, exists := snapshot.EventIDs[event.ID]; exists && expiresAt.After(now) {
				duplicate = true
				return nil
			}
			snapshot.EventIDs[event.ID] = now.Add(s.config.EventDedupTTL)
			return nil
		}); err != nil || duplicate {
			return false
		}
	}
	_ = s.slack.AddReaction(ctx, "eyes", event.Channel, event.TS)
	select {
	case s.queue <- work{event: event, identity: identity}:
		return true
	default:
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
				_ = s.store.Prune(s.now())
			}
		}
	}
}

func (s *Service) handle(ctx context.Context, item work) {
	lock := &s.sessionLocks[sessionShard(item.identity.Key())]
	lock.Lock()
	defer lock.Unlock()

	alerts, err := s.alertmanager.Active(ctx, item.identity.Alertname, item.identity.Namespace)
	if err != nil {
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	if len(alerts) == 0 {
		s.resolve(ctx, item)
		return
	}
	if item.identity.Alertname == "Watchdog" {
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
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	if !claimed {
		_ = s.slack.RemoveReaction(ctx, "eyes", item.event.Channel, item.event.TS)
		return
	}

	s.transition(ctx, item.event, "eyes", "hourglass_flowing_sand")
	analysis, err := s.holmes.Chat(ctx, request)
	if err != nil {
		s.transition(ctx, item.event, "hourglass_flowing_sand", "x")
		return
	}
	analysis = truncateSlack(sanitize(analysis), s.config.SlackOutputMaxChars)
	if err := s.slack.Reply(ctx, item.event.Channel, item.event.TS, analysis); err != nil {
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
		s.transition(ctx, item.event, "hourglass_flowing_sand", "x")
		return
	}
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
		_ = s.slack.RemoveReaction(ctx, "eyes", item.event.Channel, item.event.TS)
		return
	}
	threadTS := record.ThreadTS
	if threadTS == "" {
		threadTS = record.ParentTS
	}
	if err := s.slack.Reply(ctx, record.Channel, threadTS, "🟢 Alertmanager confirms this alert is resolved."); err != nil {
		s.transition(ctx, item.event, "eyes", "x")
		return
	}
	_ = s.slack.RemoveReaction(ctx, "eyes", item.event.Channel, item.event.TS)
	_ = s.slack.AddReaction(ctx, "large_green_circle", item.event.Channel, item.event.TS)
	_ = s.slack.AddReaction(ctx, "large_green_circle", record.Channel, record.ParentTS)
	now := s.now()
	if err := s.store.Update(func(snapshot *session.Snapshot) error {
		record := snapshot.Sessions[item.identity.Key()]
		record.State = "resolved"
		record.UpdatedAt = now
		record.ExpiresAt = now.Add(s.config.ResolvedSessionTTL)
		snapshot.Sessions[item.identity.Key()] = record
		return nil
	}); err != nil {
		_ = s.slack.AddReaction(ctx, "x", item.event.Channel, item.event.TS)
	}
}

func (s *Service) transition(ctx context.Context, event Event, remove, add string) {
	if remove != "" {
		_ = s.slack.RemoveReaction(ctx, remove, event.Channel, event.TS)
	}
	if add != "" {
		_ = s.slack.AddReaction(ctx, add, event.Channel, event.TS)
	}
}
