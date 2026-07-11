package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/marker"
)

func TestBoundAlertsProducesCompleteBoundedJSON(t *testing.T) {
	alerts := []alertmanager.Alert{
		{Labels: map[string]string{"alertname": "A", "namespace": "ns", "pod": strings.Repeat("x", 100)}},
		{Labels: map[string]string{"alertname": "A", "namespace": "ns", "pod": strings.Repeat("y", 100)}},
	}
	data := boundAlerts(marker.Alert{Alertname: "A", Namespace: "ns"}, alerts, 240)
	if len(data) > 240 || !json.Valid(data) {
		t.Fatalf("bounded JSON is invalid or oversized: %d %q", len(data), data)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	identity := payload["identity"].(map[string]any)
	if identity["alertname"] != "A" || identity["namespace"] != "ns" || payload["truncated"] != true {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestBoundAlertsHandlesTinyLimitWithValidJSON(t *testing.T) {
	data := boundAlerts(marker.Alert{Alertname: strings.Repeat("A", 100)}, nil, 2)
	if len(data) > 2 || !json.Valid(data) {
		t.Fatalf("bounded JSON = %d bytes %q", len(data), data)
	}
}

func TestBoundRunbooksDeduplicatesAndPreservesUTF8(t *testing.T) {
	alerts := []alertmanager.Alert{
		{Annotations: map[string]string{"runbook": "检查 CPU"}},
		{Annotations: map[string]string{"runbook": "检查 CPU"}},
		{Annotations: map[string]string{"runbook": "second"}},
	}
	got := boundRunbooks(alerts, len("检查 CPU")+1)
	if got != "检查 CPU" {
		t.Fatalf("runbooks = %q", got)
	}
}

func TestSanitizeAndTruncateSlack(t *testing.T) {
	got := sanitize("Authorization: Bearer abc123 token=secret password: hunter2 api_key=key")
	for _, secret := range []string{"abc123", "=secret", "hunter2", "=key"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized output contains %q: %q", secret, got)
		}
	}
	truncated := truncateSlack(strings.Repeat("你好", 20), 32)
	if len([]rune(truncated)) != 32 || !strings.Contains(truncated, "truncated by AlertLens") {
		t.Fatalf("truncated output = %q (%d runes)", truncated, len([]rune(truncated)))
	}
}

func TestTruncateBytesKeepsUTF8Valid(t *testing.T) {
	if got := truncateBytes("A你好", 4); got != "A你" {
		t.Fatalf("truncateBytes() = %q", got)
	}
}
