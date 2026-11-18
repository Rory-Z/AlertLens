package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/slackmsg"
)

func (s *Service) runHolmes(
	ctx context.Context, event slackmsg.Event, replyThreadTS string, request holmes.Request, currentReaction string,
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
		parts = slackmsg.SplitOverflow(answer)
	} else {
		parts = slackmsg.Split(answer)
	}
	for index, part := range parts[:min(len(parts), slackmsg.MaxAnswerParts)] {
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
				"Ask AlertLens a narrower follow-up question.", err, slackmsg.MaxAnswerParts))
	}
	if len(parts) > slackmsg.MaxAnswerParts {
		return s.failHolmesAnswerDelivery(ctx, event, replyThreadTS,
			fmt.Sprintf("The answer is incomplete; answer has %d parts; only the first %d were delivered. "+
				"Ask AlertLens a narrower follow-up question.", len(parts), slackmsg.MaxAnswerParts))
	}
	s.transition(ctx, event, "hourglass_flowing_sand", "white_check_mark")
	return true
}

func (s *Service) failHolmesRequest(
	ctx context.Context, event slackmsg.Event, replyThreadTS string, err error,
) bool {
	s.metrics.Event("failed")
	slog.Error("Holmes request failed", "reason", sanitize(err.Error()))
	replyCtx := ctx
	message := slackmsg.HolmesFailureReplyPrefix + " " + sanitize(err.Error())
	cancelReply := func() {}
	if ctx.Err() != nil {
		replyCtx, cancelReply = context.WithTimeout(context.Background(), defaultShutdownReplyTimeout)
		message = slackmsg.ShutdownReply
	}
	_ = s.slack.Reply(replyCtx, event.Channel, replyThreadTS, slackmsg.Truncate(message))
	s.transition(replyCtx, event, "hourglass_flowing_sand", "x")
	cancelReply()
	return false
}

func (s *Service) failHolmesAnswerDelivery(
	ctx context.Context, event slackmsg.Event, replyThreadTS, reason string,
) bool {
	s.metrics.Event("failed")
	slog.Error("Holmes answer delivery failed", "reason", sanitize(reason))
	_ = s.slack.Reply(ctx, event.Channel, replyThreadTS,
		slackmsg.Truncate(slackmsg.HolmesAnswerDeliveryFailureReplyPrefix+" "+reason))
	s.transition(ctx, event, "hourglass_flowing_sand", "x")
	return false
}

func (s *Service) transition(ctx context.Context, event slackmsg.Event, remove, add string) {
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
