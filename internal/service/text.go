package service

import (
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

var (
	bearerPattern     = regexp.MustCompile(`(?i)(bearer\s+)[^\s]+`)
	secretPattern     = regexp.MustCompile(`(?i)\b(token|password|secret|api[_-]?key)\s*[:=]\s*[^\s]+`)
	slackTokenPattern = regexp.MustCompile(`(?i)\bx(?:oxb|app)-[a-z0-9_-]+`)
)

func jsonString(text string) string {
	data, _ := json.Marshal(text)
	return string(data)
}

func sanitize(text string) string {
	text = bearerPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = secretPattern.ReplaceAllString(text, "${1}=[REDACTED]")
	return slackTokenPattern.ReplaceAllString(text, "[REDACTED]")
}

func truncateBytes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	text = text[:maxBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
