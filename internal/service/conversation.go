package service

import (
	"github.com/emqx/alertlens/internal/holmes"
)

type ConversationMessage struct {
	Role    string
	Content string
}

func boundConversation(messages []ConversationMessage, maxBytes int) []ConversationMessage {
	if len(messages) == 0 || maxBytes <= 0 {
		return nil
	}
	root := messages[0]
	root.Content = truncateBytes(sanitize(root.Content), maxBytes)
	result := []ConversationMessage{root}
	remaining := maxBytes - len(root.Content)
	newest := make([]ConversationMessage, 0, len(messages)-1)
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

func conversationHistory(messages []ConversationMessage, prompt string) []holmes.Message {
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
