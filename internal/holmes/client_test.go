package holmes

import (
	"context"
	"encoding/json"
	"errors"
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

func TestChatParsesHolmesResponseFieldsInAnyOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
          "metadata":{"ok":true,"cost":-1.25e+3,"missing":null},
          "conversation_history":[{"role":"tool","content":"ignored"}],
          "analysis":"root 中 😀",
          "tool_calls":[],
          "follow_up_actions":{}
        }`)
	}))
	defer server.Close()

	got, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"})
	if err != nil || got != "root 中 😀" {
		t.Fatalf("Chat() = (%q, %v)", got, err)
	}
}

func TestChatDecodesJSONEscapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
          "metadata":[false,true,null,0,-12.5e+2,{},[]],
          "analysis":"quote: \" slash: \\ solidus: \/ controls: \b\f\n\r\t unicode: \u4e2d emoji: \ud83d\ude00 replacement: \udc00"
        }`)
	}))
	defer server.Close()

	got, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"})
	want := "quote: \" slash: \\ solidus: / controls: \b\f\n\r\t unicode: 中 emoji: 😀 replacement: �"
	if err != nil || got != want {
		t.Fatalf("Chat() = (%q, %v), want %q", got, err, want)
	}
}

func TestChatIgnoresOversizedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"analysis":"root cause","conversation_history":[{"role":"tool","content":"`+
			strings.Repeat("x", 4<<20)+`"}]}`)
	}))
	defer server.Close()

	got, err := New(holmesURL(t, server.URL), 15*time.Second).Chat(context.Background(), Request{Ask: "x"})
	if err != nil || got != "root cause" {
		t.Fatalf("Chat() = (%q, %v)", got, err)
	}
}

func TestChatMeasuresDecodedAnalysisBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"analysis":"`+strings.Repeat(`\u0061`, 1<<20)+`"}`)
	}))
	defer server.Close()

	got, err := New(holmesURL(t, server.URL), 15*time.Second).Chat(context.Background(), Request{Ask: "x"})
	if err != nil || len(got) != 1<<20 {
		t.Fatalf("Chat() = (%d bytes, %v)", len(got), err)
	}
}

func TestChatReturnsAnalysisWithMalformedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"analysis":"root cause","conversation_history":[`)
	}))
	defer server.Close()

	got, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"})
	if got != "root cause" || err == nil || !strings.Contains(err.Error(), "decode Holmes response") {
		t.Fatalf("Chat() = (%q, %v)", got, err)
	}
}

func TestChatBoundsOversizedAnalysisReads(t *testing.T) {
	client := New(holmesURL(t, "http://holmes.invalid"), 15*time.Second)
	client.http.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		body := `{"analysis":"` + strings.Repeat("x", 4<<20+1) + `"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(&maxReadSizeReader{
				reader: strings.NewReader(body),
				max:    64 << 10,
			}),
		}, nil
	})

	got, err := client.Chat(context.Background(), Request{Ask: "x"})
	if got == "" || !errors.Is(err, ErrAnalysisTooLarge) {
		t.Fatalf("Chat() = (%d bytes, %v)", len(got), err)
	}
}

func TestChatDoesNotReturnIncompleteOrEmptyAnalysis(t *testing.T) {
	for _, body := range []string{
		`{"analysis":"partial`,
		`{"analysis":"   ","conversation_history":[`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		got, err := New(holmesURL(t, server.URL), time.Second).Chat(context.Background(), Request{Ask: "x"})
		server.Close()
		if got != "" || err == nil {
			t.Fatalf("Chat() = (%q, %v) for %q", got, err, body)
		}
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
		want string
	}{
		{name: "malformed", body: "{", want: "decode Holmes response"},
		{name: "empty analysis", body: `{"analysis":""}`, want: "empty analysis"},
		{name: "oversized", body: `{"analysis":"` + strings.Repeat("x", 4<<20+1) + `"}`,
			want: "Holmes analysis exceeds 4194304 bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			if _, err := New(holmesURL(t, server.URL), 15*time.Second).Chat(
				context.Background(), Request{Ask: "x"},
			); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
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

type maxReadSizeReader struct {
	reader io.Reader
	max    int
}

func (r *maxReadSizeReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		return 0, io.ErrShortBuffer
	}
	return r.reader.Read(p)
}

func holmesURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
