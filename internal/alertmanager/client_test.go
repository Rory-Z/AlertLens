package alertmanager

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const alertResponse = `[
  {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-0"},"annotations":{"runbook":"check cpu"},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prom/graph"},
  {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-1"},"annotations":{"runbook":"check cpu"},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prom/graph"},
  {"labels":{"alertname":"ClusterDown"},"annotations":{},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":""},
  {"labels":{"alertname":"Other","namespace":"prod"},"annotations":{},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":""}
]`

func TestActiveQueriesAndFiltersAlerts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/alerts" ||
			r.URL.Query().Get("active") != "true" ||
			r.URL.Query().Has("silenced") || r.URL.Query().Has("inhibited") {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = io.WriteString(w, alertResponse)
	}))
	defer server.Close()

	alerts, err := New(mustURL(t, server.URL), time.Second).Active(context.Background(), "HighCPU", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 || alerts[0].Labels["pod"] != "api-0" || alerts[1].Annotations["runbook"] != "check cpu" {
		t.Fatalf("alerts = %#v", alerts)
	}
	if alerts[0].StartsAt.IsZero() || alerts[0].GeneratorURL != "http://prom/graph" {
		t.Fatalf("alert fields = %#v", alerts[0])
	}
}

func TestActiveMatchesMissingNamespaceAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, alertResponse)
	}))
	defer server.Close()
	alerts, err := New(mustURL(t, server.URL), time.Second).Active(context.Background(), "ClusterDown", "")
	if err != nil || len(alerts) != 1 {
		t.Fatalf("Active() = (%#v, %v)", alerts, err)
	}
}

func TestActiveRetriesServerErrorsAtMostThreeTimes(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, alertResponse)
	}))
	defer server.Close()
	alerts, err := New(mustURL(t, server.URL), time.Second).Active(context.Background(), "HighCPU", "prod")
	if err != nil || len(alerts) != 2 || attempts.Load() != 3 {
		t.Fatalf("Active() = (%d alerts, %v), attempts = %d", len(alerts), err, attempts.Load())
	}
}

func TestActiveDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	if _, err := New(mustURL(t, server.URL), time.Second).Active(context.Background(), "HighCPU", "prod"); err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestActiveRejectsBadResponses(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: "{"},
		{name: "oversized", body: `[{"labels":{}}]` + strings.Repeat(" ", 4<<20)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			if _, err := New(mustURL(t, server.URL), time.Second).Active(context.Background(), "A", ""); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestActiveHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "[]")
	}))
	defer server.Close()
	if _, err := New(mustURL(t, server.URL), time.Millisecond).Active(context.Background(), "A", ""); err == nil {
		t.Fatal("expected error")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
