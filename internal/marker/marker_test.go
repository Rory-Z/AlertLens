package marker

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Alert
		ok   bool
	}{
		{
			name: "new",
			text: `alert <!-- alertlens:alertname=HighCPU,namespace=prod -->`,
			want: Alert{Alertname: "HighCPU", Namespace: "prod"},
			ok:   true,
		},
		{
			name: "legacy ignores status",
			text: `<!-- vigil:alertname=HighCPU,namespace=prod,status=resolved -->`,
			want: Alert{Alertname: "HighCPU", Namespace: "prod"},
			ok:   true,
		},
		{
			name: "escaped",
			text: `&lt;!-- alertlens:alertname=Watchdog,namespace= --&gt;`,
			want: Alert{Alertname: "Watchdog"},
			ok:   true,
		},
		{
			name: "whitespace and newlines",
			text: "<!--\n alertlens: alertname = PodDown , namespace = staging \n-->",
			want: Alert{Alertname: "PodDown", Namespace: "staging"},
			ok:   true,
		},
		{name: "missing namespace", text: `<!-- alertlens:alertname=HighCPU -->`},
		{name: "empty alert name", text: `<!-- alertlens:alertname=,namespace=prod -->`},
		{name: "malformed pairs", text: `<!-- alertlens:alertname,namespace -->`},
		{name: "unrelated", text: "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("Parse() = (%#v, %v), want (%#v, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAlertKey(t *testing.T) {
	if got := (Alert{Alertname: "NodeDown"}).Key(); got != "am:NodeDown:" {
		t.Fatalf("Key() = %q", got)
	}
}
