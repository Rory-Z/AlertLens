package holmes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxAnalysisBytes = 4 << 20

var ErrAnalysisTooLarge = fmt.Errorf("holmes analysis exceeds %d bytes", maxAnalysisBytes)
var ErrInvalidResponse = errors.New("invalid Holmes response")

type responseError struct {
	cause            error
	analysisTooLarge bool
}

func (e *responseError) Error() string { return "decode Holmes response: " + e.cause.Error() }

func (e *responseError) Is(target error) bool {
	return target == ErrInvalidResponse || e.analysisTooLarge && target == ErrAnalysisTooLarge
}

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
	analysis, analysisTooLarge, err := decodeResponse(resp.Body)
	if strings.TrimSpace(analysis) == "" {
		analysis = ""
	}
	if err != nil {
		return analysis, &responseError{cause: err, analysisTooLarge: analysisTooLarge}
	}
	if analysisTooLarge {
		return analysis, ErrAnalysisTooLarge
	}
	if analysis == "" {
		return "", errors.New("Holmes response has empty analysis")
	}
	return analysis, nil
}
