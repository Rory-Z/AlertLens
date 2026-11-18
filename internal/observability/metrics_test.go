package observability

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestMetricsExposeBoundedLabelsAndValues(t *testing.T) {
	metrics := New()
	metrics.Event("accepted")
	metrics.Event("accepted")
	metrics.Reaction("add", "success")
	metrics.Alertmanager("success", 1500*time.Millisecond)
	metrics.Holmes("error", 2500*time.Millisecond)
	metrics.ScheduledInvestigation("success")
	metrics.QueueDepth(7)
	metrics.QueueDepth(2)
	metrics.HolmesActive(2)
	metrics.HolmesActive(-1)

	w := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		kind   string
		labels map[string]string
		value  float64
	}{
		"alertlens_events_total":                          {"COUNTER", map[string]string{"outcome": "accepted"}, 2},
		"alertlens_reactions_total":                       {"COUNTER", map[string]string{"operation": "add", "outcome": "success"}, 1},
		"alertlens_alertmanager_requests_total":           {"COUNTER", map[string]string{"outcome": "success"}, 1},
		"alertlens_alertmanager_request_duration_seconds": {"HISTOGRAM", nil, 1.5},
		"alertlens_holmes_requests_total":                 {"COUNTER", map[string]string{"outcome": "error"}, 1},
		"alertlens_holmes_request_duration_seconds":       {"HISTOGRAM", nil, 2.5},
		"alertlens_scheduled_investigations_total":        {"COUNTER", map[string]string{"outcome": "success"}, 1},
		"alertlens_queue_depth":                           {"GAUGE", nil, 2},
		"alertlens_holmes_active":                         {"GAUGE", nil, 1},
	}
	if len(families) != len(want) {
		t.Fatalf("metric families = %d, want %d", len(families), len(want))
	}
	for name, expected := range want {
		family := families[name]
		if family == nil || len(family.Metric) != 1 || family.GetType().String() != expected.kind {
			t.Errorf("metric %s = %v", name, family)
			continue
		}
		metric := family.Metric[0]
		labels := make(map[string]string, len(metric.Label))
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		if !maps.Equal(labels, expected.labels) {
			t.Errorf("metric %s labels = %v, want %v", name, labels, expected.labels)
		}
		var value float64
		switch expected.kind {
		case "COUNTER":
			value = metric.GetCounter().GetValue()
		case "GAUGE":
			value = metric.GetGauge().GetValue()
		case "HISTOGRAM":
			value = metric.GetHistogram().GetSampleSum()
			if metric.GetHistogram().GetSampleCount() != 1 {
				t.Errorf("metric %s count = %d, want 1", name, metric.GetHistogram().GetSampleCount())
			}
		}
		if value != expected.value {
			t.Errorf("metric %s value = %v, want %v", name, value, expected.value)
		}
	}
}
