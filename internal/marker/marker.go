package marker

import (
	"html"
	"regexp"
	"strings"
)

var markerPattern = regexp.MustCompile(`(?s)<!--\s*(?:alertlens|vigil)\s*:(.*?)-->`)

type Alert struct {
	Alertname string
	Namespace string
}

func (a Alert) Key() string {
	return "am:" + a.Alertname + ":" + a.Namespace
}

func Parse(text string) (Alert, bool) {
	match := markerPattern.FindStringSubmatch(html.UnescapeString(text))
	if match == nil {
		return Alert{}, false
	}

	fields := make(map[string]string)
	for _, pair := range strings.Split(match[1], ",") {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	alertname, hasAlertname := fields["alertname"]
	namespace, hasNamespace := fields["namespace"]
	if !hasAlertname || alertname == "" || !hasNamespace {
		return Alert{}, false
	}
	return Alert{Alertname: alertname, Namespace: namespace}, true
}
