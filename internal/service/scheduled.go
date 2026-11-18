package service

import (
	"context"
	"log/slog"

	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/scheduled"
	"github.com/emqx/alertlens/internal/slackmsg"
)

func (s *Service) SubmitScheduled(ctx context.Context, investigation scheduled.Investigation) bool {
	rootTS, err := s.slack.Post(ctx, s.config.MonitoredChannel, "Scheduled investigation started: "+investigation.Name)
	if err != nil || rootTS == "" {
		s.recordScheduled(investigation, "failed")
		return false
	}
	event := slackmsg.Event{Channel: s.config.MonitoredChannel, TS: rootTS}
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
	ctx context.Context, event slackmsg.Event, investigation scheduled.Investigation, reason string,
) {
	_ = s.slack.Reply(ctx, event.Channel, event.TS, slackmsg.Truncate(
		slackmsg.ScheduledFailureReplyPrefix+" "+reason))
	s.transition(ctx, event, "", "x")
	s.recordScheduled(investigation, "failed")
}

func (s *Service) failScheduledShutdown(ctx context.Context, item work) {
	if item.scheduledInvestigation == nil {
		return
	}
	_ = s.slack.Reply(ctx, item.event.Channel, item.event.TS,
		slackmsg.Truncate(slackmsg.ShutdownReply))
	s.transition(ctx, item.event, "", "x")
	s.recordScheduled(*item.scheduledInvestigation, "failed")
}

func (s *Service) handleScheduled(ctx context.Context, event slackmsg.Event, investigation scheduled.Investigation) {
	unlock := s.lockThread(event.Channel, event.TS)
	defer unlock()
	request := holmes.Request{
		Ask:                    investigation.Prompt,
		Model:                  investigation.Model,
		AdditionalSystemPrompt: scheduledHolmesSystemPrompt(s.config.HolmesResponseLanguage),
		RequestSource:          "scheduled_investigation",
		SourceRef:              "schedule:" + investigation.Name,
		ConversationID:         slackmsg.ConversationID(event.Channel, event.TS),
	}
	outcome := "failed"
	if s.runHolmes(ctx, event, event.TS, request, "") {
		outcome = "success"
	}
	s.recordScheduled(investigation, outcome)
}

func (s *Service) recordScheduled(investigation scheduled.Investigation, outcome string) {
	s.metrics.ScheduledInvestigation(outcome)
	log := slog.Error
	message := "scheduled investigation failed"
	if outcome == "success" {
		log = slog.Info
		message = "scheduled investigation completed"
	}
	log(message, "name", investigation.Name, "schedule", investigation.Schedule(), "model", investigation.Model)
}
