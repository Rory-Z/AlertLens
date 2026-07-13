package service

import (
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/session"
)

func boundConversation(turns []session.ConversationTurn, maxTurns, maxBytes int) []session.ConversationTurn {
	if maxTurns <= 0 || maxBytes <= 0 {
		return nil
	}
	result := make([]session.ConversationTurn, 0, maxTurns)
	remaining := maxBytes
	for i := len(turns) - 1; i >= 0 && len(result) < maxTurns && remaining > 0; i-- {
		turn := turns[i]
		turn.Content = truncateBytes(turn.Content, remaining)
		remaining -= len(turn.Content)
		result = append(result, turn)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func conversationHistory(turns []session.ConversationTurn) []holmes.Message {
	if len(turns) == 0 {
		return nil
	}
	history := make([]holmes.Message, 1, len(turns)+1)
	history[0] = holmes.Message{Role: "system", Content: investigationSystemPrompt}
	for _, turn := range turns {
		history = append(history, holmes.Message{Role: turn.Role, Content: sanitize(turn.Content)})
	}
	return history
}
