package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	SlackBotToken        string
	SlackAppToken        string
	AlertChannels        map[string]bool
	AlertmanagerURL      *url.URL
	HolmesURL            *url.URL
	StatePath            string
	ReplyLanguage        string
	AlertmanagerTimeout  time.Duration
	HolmesTimeout        time.Duration
	HolmesMaxConcurrency int
	EventQueueSize       int
	EventDedupTTL        time.Duration
	AlertSessionTTL      time.Duration
	ResolvedSessionTTL   time.Duration
	AdhocSessionTTL      time.Duration
	AlertPayloadMaxBytes int
	RunbookMaxBytes      int
	ConversationMaxTurns int
	ConversationMaxBytes int
	SlackOutputMaxChars  int
	MetricsAddr          string
}

func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var err error

	if cfg.SlackBotToken, err = required(getenv, "SLACK_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.SlackAppToken, err = required(getenv, "SLACK_APP_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.AlertChannels, err = channels(getenv("SLACK_ALERT_CHANNELS")); err != nil {
		return Config{}, fmt.Errorf("SLACK_ALERT_CHANNELS: %w", err)
	}
	if cfg.AlertmanagerURL, err = baseURL(getenv, "ALERTMANAGER_URL"); err != nil {
		return Config{}, err
	}
	if cfg.HolmesURL, err = baseURL(getenv, "HOLMESGPT_URL"); err != nil {
		return Config{}, err
	}

	cfg.StatePath = value(getenv, "STATE_PATH", "/var/lib/alertlens/state.json")
	cfg.ReplyLanguage = value(getenv, "REPLY_LANGUAGE", "en")
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
	if cfg.EventDedupTTL, err = duration(getenv, "EVENT_DEDUP_TTL", 10*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.AlertSessionTTL, err = duration(getenv, "ALERT_SESSION_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.ResolvedSessionTTL, err = duration(getenv, "RESOLVED_SESSION_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.AdhocSessionTTL, err = duration(getenv, "ADHOC_SESSION_TTL", 8*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.AlertPayloadMaxBytes, err = positiveInt(getenv, "ALERT_PAYLOAD_MAX_BYTES", 32768); err != nil {
		return Config{}, err
	}
	if cfg.RunbookMaxBytes, err = positiveInt(getenv, "RUNBOOK_MAX_BYTES", 8192); err != nil {
		return Config{}, err
	}
	if cfg.ConversationMaxTurns, err = positiveInt(getenv, "CONVERSATION_MAX_TURNS", 6); err != nil {
		return Config{}, err
	}
	if cfg.ConversationMaxBytes, err = positiveInt(getenv, "CONVERSATION_MAX_BYTES", 16384); err != nil {
		return Config{}, err
	}
	if cfg.SlackOutputMaxChars, err = positiveInt(getenv, "SLACK_OUTPUT_MAX_CHARS", 2500); err != nil {
		return Config{}, err
	}
	cfg.MetricsAddr = value(getenv, "METRICS_ADDR", ":9090")

	return cfg, nil
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

func channels(raw string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result[item] = true
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one channel is required")
	}
	return result, nil
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
