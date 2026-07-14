package alertmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBytes = 4 << 20
	maxAttempts      = 3
)

type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

type Client struct {
	baseURL url.URL
	http    http.Client
}

func New(baseURL *url.URL, timeout time.Duration) *Client {
	return &Client{baseURL: *baseURL, http: http.Client{Timeout: timeout}}
}

func (c *Client) Active(ctx context.Context, alertname, namespace string) ([]Alert, error) {
	for attempt := 0; ; attempt++ {
		alerts, retry, err := c.activeOnce(ctx, alertname, namespace)
		if !retry || err == nil {
			return alerts, err
		}
		if attempt+1 == maxAttempts {
			return nil, err
		}
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) activeOnce(ctx context.Context, alertname, namespace string) ([]Alert, bool, error) {
	endpoint := c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v2/alerts"
	query := endpoint.Query()
	query.Set("active", "true")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("build Alertmanager request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("query Alertmanager: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode >= 500, fmt.Errorf("Alertmanager returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read Alertmanager response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, false, fmt.Errorf("Alertmanager response exceeds %d bytes", maxResponseBytes)
	}
	var all []Alert
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, false, fmt.Errorf("decode Alertmanager response: %w", err)
	}
	matched := make([]Alert, 0, len(all))
	for _, alert := range all {
		if alert.Labels["alertname"] == alertname && alert.Labels["namespace"] == namespace {
			matched = append(matched, alert)
		}
	}
	return matched, false, nil
}
