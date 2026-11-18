package service

import (
	"context"

	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/slackmsg"
)

func (s *Service) handleAsk(ctx context.Context, event slackmsg.Event) {
	parentTS := event.ThreadTS
	if parentTS == "" {
		parentTS = event.TS
	}
	unlock := s.lockThread(event.Channel, parentTS)
	defer unlock()

	var messages []slackmsg.Message
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
	key := slackmsg.ConversationID(event.Channel, parentTS)
	prompt := holmesSystemPrompt(s.config.HolmesResponseLanguage)
	request := holmes.Request{
		Ask:                    "<untrusted_user_question>\n" + jsonString(question) + "\n</untrusted_user_question>",
		ConversationHistory:    conversationHistory(messages, prompt),
		AdditionalSystemPrompt: prompt,
		RequestSource:          "freeform",
		SourceRef:              key,
		ConversationID:         key,
	}

	if !s.runHolmes(ctx, event, parentTS, request, "eyes") {
		return
	}
	s.metrics.Event("freeform")
}

func boundConversation(messages []slackmsg.Message, maxBytes int) []slackmsg.Message {
	if len(messages) == 0 || maxBytes <= 0 {
		return nil
	}
	root := messages[0]
	root.Content = truncateBytes(sanitize(root.Content), maxBytes)
	result := []slackmsg.Message{root}
	remaining := maxBytes - len(root.Content)
	newest := make([]slackmsg.Message, 0, len(messages)-1)
	for index := len(messages) - 1; index > 0; index-- {
		if remaining == 0 {
			break
		}
		message := messages[index]
		message.Content = sanitize(message.Content)
		if len(message.Content) > remaining {
			message.Content = truncateBytes(message.Content, remaining)
		}
		remaining -= len(message.Content)
		newest = append(newest, message)
	}
	for index := len(newest) - 1; index >= 0; index-- {
		result = append(result, newest[index])
	}
	return result
}

func conversationHistory(messages []slackmsg.Message, prompt string) []holmes.Message {
	if len(messages) == 0 {
		return nil
	}
	history := make([]holmes.Message, 1, len(messages)+1)
	history[0] = holmes.Message{Role: "system", Content: prompt}
	for _, message := range messages {
		history = append(history, holmes.Message{Role: message.Role, Content: message.Content})
	}
	return history
}
