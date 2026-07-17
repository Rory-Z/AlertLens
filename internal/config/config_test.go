package config

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadScheduledInvestigationsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled-investigations.yaml")
	if err := os.WriteFile(path, []byte(`scheduledInvestigations:
  - name: daily health
    schedule: "0 1 * * *"
    prompt: |
      Investigate the daily health of the platform.
  - name: weekly capacity
    schedule: "30 7 * * 1"
    prompt: Check capacity trends.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validEnv()
	env["SCHEDULED_INVESTIGATIONS_FILE"] = path

	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ScheduledInvestigations) != 2 {
		t.Fatalf("scheduled investigations = %#v", cfg.ScheduledInvestigations)
	}
	if got := cfg.ScheduledInvestigations[0]; got.Name != "daily health" ||
		got.Schedule != "0 1 * * *" || got.Prompt != "Investigate the daily health of the platform.\n" {
		t.Fatalf("first scheduled investigation = %#v", got)
	}
}

func TestLoadRejectsInvalidScheduledInvestigations(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "empty document", contents: ""},
		{name: "sequence root", contents: "- name: daily\n"},
		{name: "unknown root field", contents: "other: true\n"},
		{name: "unknown entry field", contents: scheduledYAML("daily", "0 1 * * *", "super-secret", "    other: true\n")},
		{name: "empty name", contents: scheduledYAML(" ", "0 1 * * *", "super-secret", "")},
		{name: "multiline name", contents: scheduledYAML("daily\\nhealth", "0 1 * * *", "super-secret", "")},
		{name: "long name", contents: scheduledYAML(strings.Repeat("界", 81), "0 1 * * *", "super-secret", "")},
		{name: "empty schedule", contents: scheduledYAML("daily", " ", "super-secret", "")},
		{name: "six-field schedule", contents: scheduledYAML("daily", "0 0 1 * * *", "super-secret", "")},
		{name: "cron descriptor", contents: scheduledYAML("daily", "@daily", "super-secret", "")},
		{name: "per-entry timezone", contents: scheduledYAML("daily", "CRON_TZ=America/New_York 0 1 * * *", "super-secret", "")},
		{name: "empty prompt", contents: scheduledYAML("daily", "0 1 * * *", " ", "")},
		{name: "duplicate trimmed name", contents: `scheduledInvestigations:
  - name: daily
    schedule: "0 1 * * *"
    prompt: first
  - name: " daily "
    schedule: "0 2 * * *"
    prompt: super-secret
`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scheduled-investigations.yaml")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			env := validEnv()
			env["SCHEDULED_INVESTIGATIONS_FILE"] = path
			_, err := Load(mapEnv(env))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "SCHEDULED_INVESTIGATIONS_FILE") {
				t.Fatalf("error does not name the setting: %q", err)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("error leaks prompt: %q", err)
			}
		})
	}
}

func TestLoadRejectsOversizedScheduledInvestigationsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled-investigations.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validEnv()
	env["SCHEDULED_INVESTIGATIONS_FILE"] = path

	_, err := Load(mapEnv(env))
	if err == nil || !strings.Contains(err.Error(), "SCHEDULED_INVESTIGATIONS_FILE") {
		t.Fatalf("error = %v", err)
	}
}

func scheduledYAML(name, schedule, prompt, extra string) string {
	return "scheduledInvestigations:\n" +
		"  - name: \"" + name + "\"\n" +
		"    schedule: \"" + schedule + "\"\n" +
		"    prompt: \"" + prompt + "\"\n" + extra
}

func TestLoadAcceptsSingleMonitoredChannel(t *testing.T) {
	env := validEnv()
	env["SLACK_ALERT_CHANNEL"] = " C1 "

	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MonitoredChannel != "C1" {
		t.Fatalf("channel = %q", cfg.MonitoredChannel)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(mapEnv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AlertmanagerTimeout != 5*time.Second ||
		cfg.HolmesTimeout != 15*time.Minute ||
		cfg.HolmesMaxConcurrency != 4 ||
		cfg.EventQueueSize != 100 ||
		cfg.AlertPayloadMaxBytes != 32768 ||
		cfg.RunbookMaxBytes != 8192 ||
		cfg.ConversationMaxBytes != 256<<10 ||
		cfg.MetricsAddr != ":9090" ||
		cfg.HolmesResponseLanguage != "auto" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.MonitoredChannel != "C1" {
		t.Fatalf("channel = %q", cfg.MonitoredChannel)
	}
	if cfg.AlertmanagerURL.String() != "http://alertmanager:9093" || cfg.HolmesURL.String() != "http://holmes:5050" {
		t.Fatalf("URLs = %s, %s", cfg.AlertmanagerURL, cfg.HolmesURL)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := validEnv()
	env["ALERTMANAGER_TIMEOUT"] = "2s"
	env["HOLMESGPT_TIMEOUT"] = "3m"
	env["HOLMESGPT_MAX_CONCURRENCY"] = "2"
	env["EVENT_QUEUE_SIZE"] = "10"
	env["ALERT_PAYLOAD_MAX_BYTES"] = "1000"
	env["RUNBOOK_MAX_BYTES"] = "2000"
	env["CONVERSATION_MAX_BYTES"] = "4000"
	env["METRICS_ADDR"] = "127.0.0.1:0"
	env["HOLMES_RESPONSE_LANGUAGE"] = " zh-CN "

	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AlertmanagerTimeout != 2*time.Second || cfg.HolmesTimeout != 3*time.Minute ||
		cfg.HolmesMaxConcurrency != 2 || cfg.EventQueueSize != 10 ||
		cfg.AlertPayloadMaxBytes != 1000 || cfg.RunbookMaxBytes != 2000 ||
		cfg.ConversationMaxBytes != 4000 ||
		cfg.MetricsAddr != "127.0.0.1:0" ||
		cfg.HolmesResponseLanguage != "zh-CN" {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValuesWithoutLeakingSecrets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing bot token", key: "SLACK_BOT_TOKEN"},
		{name: "missing app token", key: "SLACK_APP_TOKEN"},
		{name: "missing required value", key: "HOLMESGPT_URL"},
		{name: "missing Alertmanager URL", key: "ALERTMANAGER_URL"},
		{name: "empty channel", key: "SLACK_ALERT_CHANNEL", value: " "},
		{name: "multiple channels", key: "SLACK_ALERT_CHANNEL", value: "C1,C2"},
		{name: "space separated channels", key: "SLACK_ALERT_CHANNEL", value: "C1 C2"},
		{name: "Helm list", key: "SLACK_ALERT_CHANNEL", value: "[C1 C2]"},
		{name: "invalid URL", key: "ALERTMANAGER_URL", value: "://bad"},
		{name: "URL scheme", key: "ALERTMANAGER_URL", value: "ftp://alertmanager"},
		{name: "Holmes URL", key: "HOLMESGPT_URL", value: "://bad"},
		{name: "Alertmanager timeout", key: "ALERTMANAGER_TIMEOUT", value: "soon"},
		{name: "Holmes timeout", key: "HOLMESGPT_TIMEOUT", value: "soon"},
		{name: "integer", key: "EVENT_QUEUE_SIZE", value: "many"},
		{name: "positive integer", key: "EVENT_QUEUE_SIZE", value: "0"},
		{name: "Holmes concurrency", key: "HOLMESGPT_MAX_CONCURRENCY", value: "0"},
		{name: "alert payload limit", key: "ALERT_PAYLOAD_MAX_BYTES", value: "0"},
		{name: "alert payload minimum", key: "ALERT_PAYLOAD_MAX_BYTES", value: "127"},
		{name: "runbook limit", key: "RUNBOOK_MAX_BYTES", value: "0"},
		{name: "conversation bytes", key: "CONVERSATION_MAX_BYTES", value: "0"},
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

func TestLoadRejectsWrongSlackTokenTypes(t *testing.T) {
	for _, key := range []string{"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			env := maps.Clone(validEnv())
			env[key] = "wrong-token-type"
			if _, err := Load(mapEnv(env)); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"SLACK_BOT_TOKEN":     "xoxb-test",
		"SLACK_APP_TOKEN":     "xapp-test",
		"SLACK_ALERT_CHANNEL": "C1",
		"ALERTMANAGER_URL":    "http://alertmanager:9093",
		"HOLMESGPT_URL":       "http://holmes:5050",
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
