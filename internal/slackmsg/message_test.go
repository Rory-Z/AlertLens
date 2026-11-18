package slackmsg_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emqx/alertlens/internal/slackmsg"
)

func TestConversationIDUsesSlackThread(t *testing.T) {
	if got := slackmsg.ConversationID("C1", "100.1"); got != "slack:C1:100.1" {
		t.Fatalf("conversation ID = %q", got)
	}
}

func TestFailureRepliesAreExcludedFromConversation(t *testing.T) {
	for _, text := range []string{
		slackmsg.AlertmanagerFailureReplyPrefix + " refused",
		"⚠️ Alertmanager enrichment failed: refused",
		slackmsg.HolmesFailureReplyPrefix + " reset",
		slackmsg.HolmesAnswerDeliveryFailureReplyPrefix + " incomplete",
		slackmsg.ScheduledFailureReplyPrefix + " queue is full",
		slackmsg.ShutdownReply,
	} {
		if !slackmsg.IsFailure(text) {
			t.Errorf("failure reply included in conversation: %q", text)
		}
	}
	for _, text := range []string{"", "normal answer", "quoted " + slackmsg.HolmesFailureReplyPrefix, slackmsg.ShutdownReply + " was the previous result"} {
		if slackmsg.IsFailure(text) {
			t.Errorf("ordinary answer excluded from conversation: %q", text)
		}
	}
}

func TestTruncatePreservesShortMessagesAndBoundsUnicode(t *testing.T) {
	for _, text := range []string{"", "answer", strings.Repeat("界", slackmsg.MaxMessageChars)} {
		if got := slackmsg.Truncate(text); got != text {
			t.Fatal("message within the character limit was changed")
		}
	}
	text := strings.Repeat("你好", slackmsg.MaxMessageChars)
	got := slackmsg.Truncate(text)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != slackmsg.MaxMessageChars ||
		!strings.HasSuffix(got, "\n\n_[truncated by AlertLens]_") {
		t.Fatalf("truncated message has %d characters or lacks its notice", utf8.RuneCountInString(got))
	}
}

func TestSplitPreservesMessagesWithinLimit(t *testing.T) {
	for _, text := range []string{"", "answer", strings.Repeat("界", slackmsg.MaxMessageChars)} {
		if got := slackmsg.Split(text); !slices.Equal(got, []string{text}) {
			t.Fatal("message within the character limit was split or changed")
		}
	}
}

func TestSplitPreservesAndNumbersLongAnswers(t *testing.T) {
	for _, tt := range []struct {
		name        string
		text        string
		firstSuffix string
		parts       int
	}{
		{name: "paragraph", text: strings.Repeat("甲", 3900) + "\n\n" + strings.Repeat("乙", 200), firstSuffix: "\n\n", parts: 2},
		{name: "CRLF paragraph", text: strings.Repeat("甲", 3900) + "\r\n\r\n" + strings.Repeat("乙", 200), firstSuffix: "\r\n\r\n", parts: 2},
		{name: "line", text: strings.Repeat("甲", 3900) + "\n" + strings.Repeat("乙", 200), firstSuffix: "\n", parts: 2},
		{name: "no separator", text: strings.Repeat("甲", 4100), parts: 2},
		{name: "double digit count", text: strings.Repeat("甲", slackmsg.MaxMessageChars*10), parts: 11},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parts := slackmsg.Split(tt.text)
			if len(parts) != tt.parts {
				t.Fatalf("parts = %d, want %d", len(parts), tt.parts)
			}
			if !strings.HasSuffix(parts[0], tt.firstSuffix) {
				t.Fatal("first part did not preserve its paragraph or line boundary")
			}
			var content strings.Builder
			for i, part := range parts {
				prefix := fmt.Sprintf("(%d/%d) ", i+1, len(parts))
				if !strings.HasPrefix(part, prefix) || !utf8.ValidString(part) || utf8.RuneCountInString(part) > slackmsg.MaxMessageChars {
					t.Fatalf("part %d is invalid, oversized, or incorrectly numbered", i+1)
				}
				content.WriteString(strings.TrimPrefix(part, prefix))
			}
			if content.String() != tt.text {
				t.Fatal("split answer did not preserve its content")
			}
		})
	}
}

func TestSplitOverflowBoundsDeliveryAndKeepsUnicodePrefix(t *testing.T) {
	for _, text := range []string{"short prefix", strings.Repeat("甲\n\n乙", slackmsg.MaxMessageChars*slackmsg.MaxAnswerParts)} {
		parts := slackmsg.SplitOverflow(text)
		if len(parts) == 0 || len(parts) > slackmsg.MaxAnswerParts {
			t.Fatalf("overflow parts = %d", len(parts))
		}
		var content strings.Builder
		for i, part := range parts {
			prefix := fmt.Sprintf("(%d/%d+) ", i+1, slackmsg.MaxAnswerParts)
			if !strings.HasPrefix(part, prefix) || !utf8.ValidString(part) || utf8.RuneCountInString(part) > slackmsg.MaxMessageChars {
				t.Fatalf("overflow part %d is invalid, oversized, or incorrectly numbered", i+1)
			}
			content.WriteString(strings.TrimPrefix(part, prefix))
		}
		if !strings.HasPrefix(text, content.String()) || content.Len() == 0 {
			t.Fatal("overflow delivery did not preserve the answer prefix")
		}
		if utf8.RuneCountInString(text) > slackmsg.MaxMessageChars*slackmsg.MaxAnswerParts && len(parts) != slackmsg.MaxAnswerParts {
			t.Fatalf("oversized answer delivered %d parts", len(parts))
		}
	}
}
