package service

import (
	"reflect"
	"testing"

	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/slackmsg"
)

func TestBoundConversationKeepsRootAndNewestWithinByteBudget(t *testing.T) {
	messages := []slackmsg.Message{
		{Role: "user", Content: "root"},
		{Role: "user", Content: "01"},
		{Role: "assistant", Content: "23"},
		{Role: "user", Content: "45"},
		{Role: "assistant", Content: "67"},
	}

	got := boundConversation(messages, 10)
	want := []slackmsg.Message{
		{Role: "user", Content: "root"},
		{Role: "assistant", Content: "23"},
		{Role: "user", Content: "45"},
		{Role: "assistant", Content: "67"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound conversation = %#v, want %#v", got, want)
	}
}

func TestBoundConversationHasNoTurnLimit(t *testing.T) {
	messages := make([]slackmsg.Message, 50)
	for i := range messages {
		messages[i] = slackmsg.Message{Role: "user", Content: "x"}
	}

	if got := boundConversation(messages, 50); len(got) != 50 {
		t.Fatalf("kept %d messages, want 50", len(got))
	}
}

func TestBoundConversationTruncatesNewestMessageAtBudget(t *testing.T) {
	messages := []slackmsg.Message{
		{Role: "user", Content: "root"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "newest message"},
	}
	want := []slackmsg.Message{
		{Role: "user", Content: "root"},
		{Role: "assistant", Content: "newest"},
	}
	if got := boundConversation(messages, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("bound conversation = %#v, want %#v", got, want)
	}
}

func TestConversationHistoryStartsWithSystemAndSanitizesMessages(t *testing.T) {
	history := conversationHistory(boundConversation(
		[]slackmsg.Message{{Role: "assistant", Content: "token: secret"}}, 1024), investigationSystemPrompt)
	want := []holmes.Message{
		{Role: "system", Content: investigationSystemPrompt},
		{Role: "assistant", Content: "token=[REDACTED]"},
	}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
}

func TestBoundConversationAppliesByteLimitAfterSanitizing(t *testing.T) {
	bounded := boundConversation([]slackmsg.Message{{Role: "user", Content: "token=x"}}, 10)
	history := conversationHistory(bounded, investigationSystemPrompt)
	if got := history[1].Content; got != "token=[RED" || len(got) > 10 {
		t.Fatalf("bounded sanitized content = %q (%d bytes)", got, len(got))
	}
}
