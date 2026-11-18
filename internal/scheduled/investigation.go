package scheduled

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v2"
)

const maxFileBytes = 1 << 20

type Investigation struct {
	Name       string
	Prompt     string
	Model      string
	expression string
	timing     cron.Schedule
}

func (i Investigation) Schedule() string { return i.expression }

func (i Investigation) Next(now time.Time) time.Time {
	return i.timing.Next(now.In(time.UTC))
}

func Load(path string) ([]Investigation, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	if len(contents) > maxFileBytes {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: must not exceed 1 MiB")
	}
	if strings.TrimSpace(string(contents)) == "" {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: must contain a YAML mapping")
	}
	var document struct {
		ScheduledInvestigations []struct {
			Name     string  `yaml:"name"`
			Schedule string  `yaml:"schedule"`
			Prompt   string  `yaml:"prompt"`
			Model    *string `yaml:"model"`
		} `yaml:"scheduledInvestigations"`
	}
	if err := yaml.UnmarshalStrict(contents, &document); err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	// YAML v2 decodes both absent and null models as nil, so check field presence separately.
	var fields struct {
		ScheduledInvestigations []yaml.MapSlice `yaml:"scheduledInvestigations"`
	}
	if err := yaml.Unmarshal(contents, &fields); err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	names := make(map[string]struct{}, len(document.ScheduledInvestigations))
	investigations := make([]Investigation, len(document.ScheduledInvestigations))
	for i, entry := range document.ScheduledInvestigations {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d name must not be empty", i+1)
		}
		if strings.ContainsAny(name, "\r\n") || utf8.RuneCountInString(name) > 80 {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d name must be a single line of at most 80 characters", i+1)
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: duplicate name %q", name)
		}
		names[name] = struct{}{}
		expression := strings.TrimSpace(entry.Schedule)
		if len(strings.Fields(expression)) != 5 {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d has invalid five-field schedule", i+1)
		}
		timing, err := parser.Parse(expression)
		if err != nil {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d has invalid five-field schedule", i+1)
		}
		if strings.TrimSpace(entry.Prompt) == "" {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d prompt must not be empty", i+1)
		}
		for _, field := range fields.ScheduledInvestigations[i] {
			if field.Key == "model" && (entry.Model == nil || strings.TrimSpace(*entry.Model) == "") {
				return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d model must not be empty", i+1)
			}
		}
		investigations[i] = Investigation{Name: name, Prompt: entry.Prompt, expression: expression, timing: timing}
		if entry.Model != nil {
			investigations[i].Model = *entry.Model
		}
	}
	return investigations, nil
}
