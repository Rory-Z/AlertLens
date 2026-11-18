package slackmsg

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxMessageChars                        = 4000
	MaxAnswerParts                         = 10
	AlertmanagerFailureReplyPrefix         = "⚠️ Alertmanager verification failed:"
	HolmesFailureReplyPrefix               = "⚠️ Holmes request failed:"
	HolmesAnswerDeliveryFailureReplyPrefix = "⚠️ Holmes answer delivery failed:"
	ScheduledFailureReplyPrefix            = "⚠️ Scheduled investigation failed:"
	ShutdownReply                          = "AlertLens shutting down"
)

type Event struct {
	Channel  string
	Text     string
	TS       string
	ThreadTS string
	Mention  bool
}

type Message struct {
	Role    string
	Content string
}

func ConversationID(channel, rootTS string) string { return "slack:" + channel + ":" + rootTS }

func IsFailure(text string) bool {
	const legacyAlertmanagerFailureReplyPrefix = "⚠️ Alertmanager enrichment failed:"
	return strings.HasPrefix(text, AlertmanagerFailureReplyPrefix) ||
		strings.HasPrefix(text, legacyAlertmanagerFailureReplyPrefix) ||
		strings.HasPrefix(text, HolmesFailureReplyPrefix) ||
		strings.HasPrefix(text, HolmesAnswerDeliveryFailureReplyPrefix) ||
		strings.HasPrefix(text, ScheduledFailureReplyPrefix) ||
		text == ShutdownReply
}

func Truncate(text string) string {
	runes := []rune(text)
	if len(runes) <= MaxMessageChars {
		return text
	}
	notice := []rune("\n\n_[truncated by AlertLens]_")
	return string(runes[:MaxMessageChars-len(notice)]) + string(notice)
}

func Split(text string) []string {
	runes := []rune(text)
	if len(runes) <= MaxMessageChars {
		return []string{text}
	}
	prefixChars := 0
	var parts []string
	for {
		parts = splitSlackContent(runes, MaxMessageChars-prefixChars)
		nextPrefixChars := utf8.RuneCountInString(fmt.Sprintf("(%d/%d) ", len(parts), len(parts)))
		if nextPrefixChars == prefixChars {
			break
		}
		prefixChars = nextPrefixChars
	}
	for index := range parts {
		parts[index] = fmt.Sprintf("(%d/%d) %s", index+1, len(parts), parts[index])
	}
	return parts
}

func SplitOverflow(text string) []string {
	prefixChars := utf8.RuneCountInString(fmt.Sprintf("(%d/%d+) ", MaxAnswerParts, MaxAnswerParts))
	maxChars := MaxMessageChars - prefixChars
	runes := []rune(firstRunes(text, MaxAnswerParts*maxChars))
	parts := splitSlackContent(runes, maxChars)
	parts = parts[:min(len(parts), MaxAnswerParts)]
	for index := range parts {
		parts[index] = fmt.Sprintf("(%d/%d+) %s", index+1, MaxAnswerParts, parts[index])
	}
	return parts
}

func firstRunes(text string, max int) string {
	count := 0
	for index := range text {
		if count == max {
			return text[:index]
		}
		count++
	}
	return text
}

func splitSlackContent(runes []rune, maxChars int) []string {
	var parts []string
	for len(runes) > maxChars {
		cut := slackSplitPoint(runes, maxChars)
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	return append(parts, string(runes))
}

func slackSplitPoint(runes []rune, maxChars int) int {
	for index := maxChars; index > 1; index-- {
		if runes[index-1] != '\n' {
			continue
		}
		previous := index - 2
		if runes[previous] == '\r' {
			previous--
		}
		if previous >= 0 && runes[previous] == '\n' {
			return index
		}
	}
	for index := maxChars; index > 0; index-- {
		if runes[index-1] == '\n' {
			return index
		}
	}
	return maxChars
}
