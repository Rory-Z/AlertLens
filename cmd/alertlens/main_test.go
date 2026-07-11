package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunRejectsInvalidConfig(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	env := validEnv(t)
	env["METRICS_ADDR"] = "bad address"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, mapEnv(env)); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunShutsDownWithContext(t *testing.T) {
	env := validEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, mapEnv(env)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env["STATE_PATH"]); err != nil {
		t.Fatalf("state was not initialized: %v", err)
	}
}

func TestRunRejectsCorruptState(t *testing.T) {
	env := validEnv(t)
	if err := os.WriteFile(env["STATE_PATH"], []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, mapEnv(env)); err == nil {
		t.Fatal("expected error")
	}
}

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"SLACK_BOT_TOKEN":      "xoxb-test",
		"SLACK_APP_TOKEN":      "xapp-test",
		"SLACK_ALERT_CHANNELS": "C1",
		"ALERTMANAGER_URL":     "http://alertmanager:9093",
		"HOLMESGPT_URL":        "http://holmes:5050",
		"STATE_PATH":           filepath.Join(t.TempDir(), "state.json"),
		"METRICS_ADDR":         "127.0.0.1:0",
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
