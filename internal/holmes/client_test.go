package holmes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatUsesHolmesAPIContractWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %s, content-type = %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			t.Fatalf("unexpected authentication header")
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if raw["ask"] != "investigate" || raw["request_source"] != "alert_investigation" ||
			raw["source_ref"] != "am:HighCPU:prod" || raw["conversation_id"] != "am:HighCPU:prod" {
			t.Fatalf("request = %#v", raw)
		}
		if _, ok := raw["model"]; ok {
			t.Fatalf("request contains model: %#v", raw)
		}
		if _, ok := raw["api_key"]; ok {
			t.Fatalf("request contains api_key: %#v", raw)
		}
		_, _ = io.WriteString(w, `{"analysis":"root cause"}`)
	}))
	defer server.Close()

	request := Request{
		Ask:                    "investigate",
		AdditionalSystemPrompt: "read only",
		RequestSource:          "alert_investigation",
		SourceRef:              "am:HighCPU:prod",
		ConversationID:         "am:HighCPU:prod",
	}
	got, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), request)
	if err != nil || got != "root cause" {
		t.Fatalf("Chat() = (%q, %v)", got, err)
	}
}

func TestChatDoesNotRetryFailures(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestChatRejectsBadResponses(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: "{"},
		{name: "empty analysis", body: `{"analysis":""}`},
		{name: "oversized", body: `{"analysis":"` + strings.Repeat("x", 4<<20) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			if _, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestChatHonorsTimeoutWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	client := New(holmesURL(t, "http://holmes.invalid"), time.Second)
	client.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Chat(ctx, Request{Ask: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func holmesURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
