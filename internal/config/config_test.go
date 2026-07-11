package config

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(mapEnv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StatePath != "/var/lib/alertlens/state.json" ||
		cfg.ReplyLanguage != "en" ||
		cfg.AlertmanagerTimeout != 5*time.Second ||
		cfg.HolmesTimeout != 15*time.Minute ||
		cfg.HolmesMaxConcurrency != 4 ||
		cfg.EventQueueSize != 100 ||
		cfg.EventDedupTTL != 10*time.Minute ||
		cfg.AlertSessionTTL != 24*time.Hour ||
		cfg.ResolvedSessionTTL != 24*time.Hour ||
		cfg.AdhocSessionTTL != 8*time.Hour ||
		cfg.AlertPayloadMaxBytes != 32768 ||
		cfg.RunbookMaxBytes != 8192 ||
		cfg.ConversationMaxTurns != 6 ||
		cfg.ConversationMaxBytes != 16384 ||
		cfg.SlackOutputMaxChars != 2500 ||
		cfg.MetricsAddr != ":9090" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !cfg.AlertChannels["C1"] || !cfg.AlertChannels["C2"] || len(cfg.AlertChannels) != 2 {
		t.Fatalf("channels = %#v", cfg.AlertChannels)
	}
	if cfg.AlertmanagerURL.String() != "http://alertmanager:9093" || cfg.HolmesURL.String() != "http://holmes:5050" {
		t.Fatalf("URLs = %s, %s", cfg.AlertmanagerURL, cfg.HolmesURL)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := validEnv()
	env["STATE_PATH"] = "/tmp/state.json"
	env["REPLY_LANGUAGE"] = "zh"
	env["ALERTMANAGER_TIMEOUT"] = "2s"
	env["HOLMESGPT_TIMEOUT"] = "3m"
	env["HOLMESGPT_MAX_CONCURRENCY"] = "2"
	env["EVENT_QUEUE_SIZE"] = "10"
	env["EVENT_DEDUP_TTL"] = "1m"
	env["ALERT_SESSION_TTL"] = "2h"
	env["RESOLVED_SESSION_TTL"] = "3h"
	env["ADHOC_SESSION_TTL"] = "4h"
	env["ALERT_PAYLOAD_MAX_BYTES"] = "1000"
	env["RUNBOOK_MAX_BYTES"] = "2000"
	env["CONVERSATION_MAX_TURNS"] = "3"
	env["CONVERSATION_MAX_BYTES"] = "4000"
	env["SLACK_OUTPUT_MAX_CHARS"] = "500"
	env["METRICS_ADDR"] = "127.0.0.1:0"

	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatePath != "/tmp/state.json" || cfg.ReplyLanguage != "zh" ||
		cfg.AlertmanagerTimeout != 2*time.Second || cfg.HolmesTimeout != 3*time.Minute ||
		cfg.HolmesMaxConcurrency != 2 || cfg.EventQueueSize != 10 ||
		cfg.EventDedupTTL != time.Minute || cfg.AlertSessionTTL != 2*time.Hour ||
		cfg.ResolvedSessionTTL != 3*time.Hour || cfg.AdhocSessionTTL != 4*time.Hour ||
		cfg.AlertPayloadMaxBytes != 1000 || cfg.RunbookMaxBytes != 2000 ||
		cfg.ConversationMaxTurns != 3 || cfg.ConversationMaxBytes != 4000 ||
		cfg.SlackOutputMaxChars != 500 || cfg.MetricsAddr != "127.0.0.1:0" {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValuesWithoutLeakingSecrets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing required value", key: "HOLMESGPT_URL"},
		{name: "empty channel list", key: "SLACK_ALERT_CHANNELS", value: " , "},
		{name: "invalid URL", key: "ALERTMANAGER_URL", value: "://bad"},
		{name: "URL scheme", key: "ALERTMANAGER_URL", value: "ftp://alertmanager"},
		{name: "duration", key: "HOLMESGPT_TIMEOUT", value: "soon"},
		{name: "positive duration", key: "EVENT_DEDUP_TTL", value: "0s"},
		{name: "integer", key: "EVENT_QUEUE_SIZE", value: "many"},
		{name: "positive integer", key: "EVENT_QUEUE_SIZE", value: "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := maps.Clone(validEnv())
			env[tt.key] = tt.value
			_, err := Load(mapEnv(env))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("error %q does not name %s", err, tt.key)
			}
			if strings.Contains(err.Error(), "xoxb-test") || strings.Contains(err.Error(), "xapp-test") {
				t.Fatalf("error leaks a secret: %q", err)
			}
		})
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_APP_TOKEN":      "xapp-test",
		"SLACK_ALERT_CHANNELS": "C1, C2,C1",
		"ALERTMANAGER_URL":     "http://alertmanager:9093",
		"HOLMESGPT_URL":        "http://holmes:5050",
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
