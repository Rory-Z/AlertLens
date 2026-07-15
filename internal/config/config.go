package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v2"
)

const scheduledInvestigationsMaxBytes = 1 << 20

type ScheduledInvestigation struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"`
	Prompt   string `yaml:"prompt"`
}

type Config struct {
	SlackBotToken           string
	SlackAppToken           string
	MonitoredChannel        string
	AlertmanagerURL         *url.URL
	HolmesURL               *url.URL
	AlertmanagerTimeout     time.Duration
	HolmesTimeout           time.Duration
	HolmesMaxConcurrency    int
	EventQueueSize          int
	AlertPayloadMaxBytes    int
	RunbookMaxBytes         int
	ConversationMaxBytes    int
	SlackOutputMaxChars     int
	HolmesResponseLanguage  string
	MetricsAddr             string
	ScheduledInvestigations []ScheduledInvestigation
}

func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var err error

	if cfg.SlackBotToken, err = required(getenv, "SLACK_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if !strings.HasPrefix(cfg.SlackBotToken, "xoxb-") {
		return Config{}, fmt.Errorf("SLACK_BOT_TOKEN: must be a bot token")
	}
	if cfg.SlackAppToken, err = required(getenv, "SLACK_APP_TOKEN"); err != nil {
		return Config{}, err
	}
	if !strings.HasPrefix(cfg.SlackAppToken, "xapp-") {
		return Config{}, fmt.Errorf("SLACK_APP_TOKEN: must be an app-level token")
	}
	if cfg.MonitoredChannel, err = required(getenv, "SLACK_ALERT_CHANNEL"); err != nil {
		return Config{}, err
	}
	cfg.MonitoredChannel = strings.TrimSpace(cfg.MonitoredChannel)
	if len(cfg.MonitoredChannel) < 2 || (cfg.MonitoredChannel[0] != 'C' && cfg.MonitoredChannel[0] != 'G') ||
		strings.IndexFunc(cfg.MonitoredChannel[1:], func(r rune) bool {
			return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
		}) >= 0 {
		return Config{}, fmt.Errorf("SLACK_ALERT_CHANNEL: must contain one channel ID")
	}
	if cfg.AlertmanagerURL, err = baseURL(getenv, "ALERTMANAGER_URL"); err != nil {
		return Config{}, err
	}
	if cfg.HolmesURL, err = baseURL(getenv, "HOLMESGPT_URL"); err != nil {
		return Config{}, err
	}

	if cfg.AlertmanagerTimeout, err = duration(getenv, "ALERTMANAGER_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HolmesTimeout, err = duration(getenv, "HOLMESGPT_TIMEOUT", 15*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.HolmesMaxConcurrency, err = positiveInt(getenv, "HOLMESGPT_MAX_CONCURRENCY", 4); err != nil {
		return Config{}, err
	}
	if cfg.EventQueueSize, err = positiveInt(getenv, "EVENT_QUEUE_SIZE", 100); err != nil {
		return Config{}, err
	}
	if cfg.AlertPayloadMaxBytes, err = positiveInt(getenv, "ALERT_PAYLOAD_MAX_BYTES", 32768); err != nil {
		return Config{}, err
	}
	if cfg.AlertPayloadMaxBytes < 128 {
		return Config{}, fmt.Errorf("ALERT_PAYLOAD_MAX_BYTES: must be at least 128")
	}
	if cfg.RunbookMaxBytes, err = positiveInt(getenv, "RUNBOOK_MAX_BYTES", 8192); err != nil {
		return Config{}, err
	}
	if cfg.ConversationMaxBytes, err = positiveInt(getenv, "CONVERSATION_MAX_BYTES", 256<<10); err != nil {
		return Config{}, err
	}
	if cfg.SlackOutputMaxChars, err = positiveInt(getenv, "SLACK_OUTPUT_MAX_CHARS", 2500); err != nil {
		return Config{}, err
	}
	cfg.HolmesResponseLanguage = strings.TrimSpace(getenv("HOLMES_RESPONSE_LANGUAGE"))
	if cfg.HolmesResponseLanguage == "" {
		cfg.HolmesResponseLanguage = "auto"
	}
	cfg.MetricsAddr = value(getenv, "METRICS_ADDR", ":9090")
	if cfg.ScheduledInvestigations, err = scheduledInvestigations(getenv("SCHEDULED_INVESTIGATIONS_FILE")); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func scheduledInvestigations(path string) ([]ScheduledInvestigation, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, scheduledInvestigationsMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	if len(contents) > scheduledInvestigationsMaxBytes {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: must not exceed 1 MiB")
	}
	if strings.TrimSpace(string(contents)) == "" {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: must contain a YAML mapping")
	}
	var document struct {
		ScheduledInvestigations []ScheduledInvestigation `yaml:"scheduledInvestigations"`
	}
	if err := yaml.UnmarshalStrict(contents, &document); err != nil {
		return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: %w", err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	names := make(map[string]struct{}, len(document.ScheduledInvestigations))
	for i := range document.ScheduledInvestigations {
		investigation := &document.ScheduledInvestigations[i]
		investigation.Name = strings.TrimSpace(investigation.Name)
		if investigation.Name == "" {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d name must not be empty", i+1)
		}
		if strings.ContainsAny(investigation.Name, "\r\n") || utf8.RuneCountInString(investigation.Name) > 80 {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d name must be a single line of at most 80 characters", i+1)
		}
		if _, duplicate := names[investigation.Name]; duplicate {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: duplicate name %q", investigation.Name)
		}
		names[investigation.Name] = struct{}{}
		investigation.Schedule = strings.TrimSpace(investigation.Schedule)
		if len(strings.Fields(investigation.Schedule)) != 5 {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d has invalid five-field schedule", i+1)
		}
		if _, err := parser.Parse(investigation.Schedule); err != nil {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d has invalid five-field schedule", i+1)
		}
		if strings.TrimSpace(investigation.Prompt) == "" {
			return nil, fmt.Errorf("SCHEDULED_INVESTIGATIONS_FILE: entry %d prompt must not be empty", i+1)
		}
	}
	return document.ScheduledInvestigations, nil
}

func required(getenv func(string) string, key string) (string, error) {
	v := getenv(key)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s: required", key)
	}
	return v, nil
}

func value(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func baseURL(getenv func(string) string, key string) (*url.URL, error) {
	raw, err := required(getenv, key)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%s: invalid HTTP URL", key)
	}
	return u, nil
}

func duration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s: must be a positive duration", key)
	}
	return v, nil
}

func positiveInt(getenv func(string) string, key string, fallback int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s: must be a positive integer", key)
	}
	return v, nil
}
