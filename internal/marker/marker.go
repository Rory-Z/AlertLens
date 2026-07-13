package marker

import (
	"html"
	"regexp"
	"strings"
)

var markerPattern = regexp.MustCompile(`(?s)<!--\s*(?:alertlens|vigil)\s*:(.*?)-->`)

type Alert struct {
	Alertname string `json:"alertname"`
	Namespace string `json:"namespace"`
	Status    string `json:"-"`
}

func (a Alert) Key() string {
	return "am:" + a.Alertname + ":" + a.Namespace
}

func Present(text string) bool {
	return markerPattern.MatchString(html.UnescapeString(text))
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
	status := fields["status"]
	if !hasAlertname || alertname == "" || !hasNamespace || (status != "firing" && status != "resolved") {
		return Alert{}, false
	}
	return Alert{Alertname: alertname, Namespace: namespace, Status: status}, true
}
