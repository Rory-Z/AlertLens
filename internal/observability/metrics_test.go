package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeBoundedLabels(t *testing.T) {
	metrics := New()
	metrics.Event("accepted")
	metrics.Reaction("add", "success")
	metrics.Alertmanager("success", time.Second)
	metrics.Holmes("error", 2*time.Second)
	metrics.PersistenceError()
	metrics.QueueDepth(2)
	metrics.HolmesActive(1)
	metrics.Sessions(3)
	metrics.Watchdog(time.Unix(123, 0))

	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"alertlens_events_total":                          false,
		"alertlens_reactions_total":                       false,
		"alertlens_alertmanager_requests_total":           false,
		"alertlens_alertmanager_request_duration_seconds": false,
		"alertlens_holmes_requests_total":                 false,
		"alertlens_holmes_request_duration_seconds":       false,
		"alertlens_persistence_errors_total":              false,
		"alertlens_queue_depth":                           false,
		"alertlens_holmes_active":                         false,
		"alertlens_sessions":                              false,
		"alertlens_watchdog_last_seen_timestamp":          false,
		"alertlens_watchdog_received_total":               false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				for _, forbidden := range []string{"alertname", "namespace", "channel", "thread", "event", "url"} {
					if strings.Contains(strings.ToLower(label.GetName()), forbidden) {
						t.Fatalf("metric %s has forbidden label %s", family.GetName(), label.GetName())
					}
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %s was not gathered", name)
		}
	}
}

func TestHandlerServesPrivateRegistry(t *testing.T) {
	metrics := New()
	metrics.Event("accepted")
	w := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `alertlens_events_total{outcome="accepted"} 1`) {
		t.Fatalf("response = %d %q", w.Code, w.Body.String())
	}
}
