package service

import (
	"strings"
	"testing"
)

func TestSanitizeRedactsCredentials(t *testing.T) {
	got := sanitize("Authorization: Bearer abc123 token=secret password: hunter2 api_key=key")
	for _, secret := range []string{"abc123", "=secret", "hunter2", "=key"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized output contains %q: %q", secret, got)
		}
	}
}

func TestSanitizeRedactsStandaloneSlackTokens(t *testing.T) {
	botToken := "xox" + "b-123456789012-123456789012-abcdefghijklmnopqrstuvwx"
	appToken := "XAP" + "P-1-A1234567890-1234567890-abcdef0123456789abcdef0123456789"
	got := sanitize("bot " + botToken + " app " + appToken)
	if strings.Contains(got, botToken) || strings.Contains(got, appToken) {
		t.Fatal("standalone Slack credential remained in sanitized text")
	}
}

func TestTruncateBytesKeepsUTF8Valid(t *testing.T) {
	if got := truncateBytes("A你好", 4); got != "A你" {
		t.Fatalf("truncateBytes() = %q", got)
	}
}
