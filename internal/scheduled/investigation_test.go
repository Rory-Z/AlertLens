package scheduled

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadedInvestigationUsesUTCSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled-investigations.yaml")
	if err := os.WriteFile(path, []byte(scheduledYAML("daily", "0 1 * * *", "Investigate health.", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	investigations, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 18, 8, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	next := investigations[0].Next(now)
	want := time.Date(2026, time.November, 18, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) || next.Location() != time.UTC {
		t.Fatalf("Next(%v) = %v, want %v in UTC", now, next, want)
	}
}

func TestLoadWithoutFileDisablesInvestigations(t *testing.T) {
	investigations, err := Load("")
	if err != nil || investigations != nil {
		t.Fatalf("Load() = (%v, %v)", investigations, err)
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
		{name: "empty model", contents: scheduledYAML("daily", "0 1 * * *", "super-secret", "    model: \"   \"\n")},
		{name: "null model", contents: scheduledYAML("daily", "0 1 * * *", "super-secret", "    model: null\n")},
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
			_, err := Load(path)
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
	_, err := Load(path)
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
