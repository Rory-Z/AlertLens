package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/marker"
	"github.com/emqx/alertlens/internal/slackmsg"
)

const verifiedAlertPrompt = " AlertLens verified immediately before this request that Alertmanager returned at least one active alert matching the supplied identity. The supplied snapshot may be truncated."

type alertPayload struct {
	Verified  bool                 `json:"verified"`
	Identity  marker.Alert         `json:"identity"`
	Alerts    []alertmanager.Alert `json:"alerts"`
	Truncated bool                 `json:"truncated"`
}

func (s *Service) handleFiring(ctx context.Context, item work) {
	unlock := s.lockThread(item.event.Channel, item.event.TS)
	defer unlock()

	started := time.Now()
	alerts, err := s.alertmanager.Active(ctx, item.identity.Alertname, item.identity.Namespace)
	if err != nil {
		s.metrics.Alertmanager("error", time.Since(started))
		s.failVerification(ctx, item.event, sanitize(err.Error()))
		return
	}
	if len(alerts) == 0 {
		s.metrics.Alertmanager("no_match", time.Since(started))
		s.failVerification(ctx, item.event, "no active alert matches Alert Identity")
		return
	}
	s.metrics.Alertmanager("success", time.Since(started))

	request, err := buildRequest(item.event, item.identity, alerts, s.config)
	if err != nil {
		s.failVerification(ctx, item.event, sanitize(err.Error()))
		return
	}
	if !s.runHolmes(ctx, item.event, item.event.TS, request, "eyes") {
		return
	}
	s.metrics.Event("firing")
}

func (s *Service) failVerification(ctx context.Context, event slackmsg.Event, reason string) {
	_ = s.slack.Reply(ctx, event.Channel, event.TS, slackmsg.Truncate(
		slackmsg.AlertmanagerFailureReplyPrefix+" "+reason))
	s.metrics.Event("failed")
	s.transition(ctx, event, "eyes", "x")
}

func buildRequest(event slackmsg.Event, identity marker.Alert, alerts []alertmanager.Alert, cfg Config) (holmes.Request, error) {
	safeIdentity := marker.Alert{Alertname: sanitize(identity.Alertname), Namespace: sanitize(identity.Namespace)}
	safeAlerts := sanitizeAlerts(alerts)
	alertJSON, err := boundAlerts(safeIdentity, safeAlerts, cfg.AlertPayloadMaxBytes)
	if err != nil {
		return holmes.Request{}, err
	}
	runbooks := jsonString(boundRunbooks(safeAlerts, cfg.RunbookMaxBytes))
	slackText := jsonString(truncateBytes(sanitize(event.Text), cfg.ConversationMaxBytes))
	ask := "<alertmanager_alerts>\n" + string(alertJSON) + "\n</alertmanager_alerts>\n" +
		"<inline_runbooks>\n" + runbooks + "\n</inline_runbooks>\n" +
		"<untrusted_slack_message>\n" + slackText + "\n</untrusted_slack_message>\n" +
		"Determine the likely root cause and give concise evidence-backed next checks."
	key := identity.Key()
	conversationID := slackmsg.ConversationID(event.Channel, event.TS)
	return holmes.Request{
		Ask:                    ask,
		AdditionalSystemPrompt: holmesSystemPrompt(cfg.HolmesResponseLanguage) + verifiedAlertPrompt,
		RequestSource:          "alert_investigation",
		SourceRef:              key,
		ConversationID:         conversationID,
	}, nil
}

func sanitizeAlerts(alerts []alertmanager.Alert) []alertmanager.Alert {
	result := make([]alertmanager.Alert, len(alerts))
	for index, alert := range alerts {
		alert.Labels = sanitizeMap(alert.Labels)
		alert.Annotations = sanitizeMap(alert.Annotations)
		alert.GeneratorURL = sanitize(alert.GeneratorURL)
		result[index] = alert
	}
	return result
}

func sanitizeMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = sanitize(value)
	}
	return result
}

func boundAlerts(identity marker.Alert, alerts []alertmanager.Alert, maxBytes int) (json.RawMessage, error) {
	payload := alertPayload{Verified: true, Identity: identity, Alerts: make([]alertmanager.Alert, 0, len(alerts))}
	for _, alert := range alerts {
		payload.Alerts = append(payload.Alerts, alert)
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode verified alert snapshot: %w", err)
		}
		if len(data) > maxBytes {
			payload.Alerts = payload.Alerts[:len(payload.Alerts)-1]
			payload.Truncated = true
			break
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode verified alert snapshot: %w", err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("verified alert snapshot exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func boundRunbooks(alerts []alertmanager.Alert, maxBytes int) string {
	seen := make(map[string]bool)
	var result string
	for _, alert := range alerts {
		runbook := strings.TrimSpace(alert.Annotations["runbook"])
		if runbook == "" || seen[runbook] {
			continue
		}
		seen[runbook] = true
		separator := ""
		if result != "" {
			separator = "\n\n---\n\n"
		}
		remaining := maxBytes - len(result) - len(separator)
		if remaining <= 0 {
			break
		}
		result += separator + truncateBytes(runbook, remaining)
		if len(runbook) > remaining {
			break
		}
	}
	return result
}
