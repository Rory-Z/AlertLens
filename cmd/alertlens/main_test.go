package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/emqx/alertlens/internal/service"
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
}

func TestServeRunsUntilCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, http.NotFoundHandler(), &fakeSlackRunner{}, fakeWorker{})
	}()
	waitHTTPStatus(t, "http://"+listener.Addr().String()+"/readyz", http.StatusOK)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeReturnsSlackError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("Slack failed")
	err = serve(context.Background(), listener, http.NotFoundHandler(), &fakeSlackRunner{runErr: want}, fakeWorker{})
	if !errors.Is(err, want) {
		t.Fatalf("serve() = %v", err)
	}
}

func TestServeRejectsUnexpectedSlackStop(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	err = serve(context.Background(), listener, http.NotFoundHandler(), &fakeSlackRunner{returnImmediately: true}, fakeWorker{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServeReturnsHTTPError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serve(context.Background(), listener, http.NotFoundHandler(), &fakeSlackRunner{}, fakeWorker{}); err == nil {
		t.Fatal("expected error")
	}
}

type fakeSlackRunner struct {
	runErr            error
	readyErr          error
	returnImmediately bool
}

func (f *fakeSlackRunner) Run(ctx context.Context, _ func(context.Context, service.Event) bool) error {
	if f.runErr != nil || f.returnImmediately {
		return f.runErr
	}
	<-ctx.Done()
	return nil
}

func (f *fakeSlackRunner) Ready() error { return f.readyErr }

type fakeWorker struct{}

func (fakeWorker) Submit(context.Context, service.Event) bool { return true }
func (fakeWorker) Run(ctx context.Context)                    { <-ctx.Done() }

func waitHTTPStatus(t *testing.T, target string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(target)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s did not return %d", target, want)
}

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"SLACK_BOT_TOKEN":     "xoxb-test",
		"SLACK_APP_TOKEN":     "xapp-test",
		"SLACK_ALERT_CHANNEL": "C1",
		"ALERTMANAGER_URL":    "http://alertmanager:9093",
		"HOLMESGPT_URL":       "http://holmes:5050",
		"METRICS_ADDR":        "127.0.0.1:0",
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
