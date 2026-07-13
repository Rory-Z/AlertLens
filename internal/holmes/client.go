package holmes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Ask                    string    `json:"ask"`
	ConversationHistory    []Message `json:"conversation_history,omitempty"`
	AdditionalSystemPrompt string    `json:"additional_system_prompt,omitempty"`
	RequestSource          string    `json:"request_source,omitempty"`
	SourceRef              string    `json:"source_ref,omitempty"`
	ConversationID         string    `json:"conversation_id,omitempty"`
}

type Client struct {
	baseURL url.URL
	http    http.Client
}

func New(baseURL *url.URL, timeout time.Duration) *Client {
	return &Client{baseURL: *baseURL, http: http.Client{Timeout: timeout}}
}

func (c *Client) Chat(ctx context.Context, request Request) (string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode Holmes request: %w", err)
	}
	endpoint := c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build Holmes request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call Holmes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Holmes returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read Holmes response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return "", fmt.Errorf("Holmes response exceeds %d bytes", maxResponseBytes)
	}
	var response struct {
		Analysis string `json:"analysis"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode Holmes response: %w", err)
	}
	if strings.TrimSpace(response.Analysis) == "" {
		return "", errors.New("Holmes response has empty analysis")
	}
	return response.Analysis, nil
}
