package service

import (
	"testing"

	"github.com/emqx/alertlens/internal/session"
)

func TestBoundConversationKeepsNewestCompleteBudget(t *testing.T) {
	if got := boundConversation(nil, 0, 0); got != nil {
		t.Fatalf("zero budget = %#v", got)
	}
	turns := make([]session.ConversationTurn, 8)
	for i := range turns {
		turns[i] = session.ConversationTurn{Role: "user", Content: "1234"}
	}
	got := boundConversation(turns, 6, 18)
	if len(got) != 5 {
		t.Fatalf("turns = %#v", got)
	}
	bytes := 0
	for _, turn := range got {
		bytes += len(turn.Content)
	}
	if bytes != 18 || len(got[0].Content) != 2 {
		t.Fatalf("bytes = %d, turns = %#v", bytes, got)
	}
}

func TestConversationHistoryStartsWithSystem(t *testing.T) {
	history := conversationHistory([]session.ConversationTurn{{Role: "assistant", Content: "prior"}})
	if len(history) != 2 || history[0].Role != "system" || history[1].Content != "prior" {
		t.Fatalf("history = %#v", history)
	}
}
