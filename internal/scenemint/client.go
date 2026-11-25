package scenemint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

const requestTimeout = 5 * time.Minute
const MaxRelogCheckEmails = 100

type RelogCandidate struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type RelogCheck struct {
	Items               []RelogCandidate `json:"items"`
	InvalidEmailIndexes []int            `json:"invalidEmailIndexes,omitempty"`
}

func NewClient(baseURL, apiKey string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if baseURL == "" || apiKey == "" {
		return nil, errors.New("SceneMint URL and API key are required")
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

func (c *Client) Upload(ctx context.Context, accessToken string) error {
	return c.postJSON(ctx, "/api/ops/accounts", map[string]string{"accessToken": accessToken}, nil)
}

func (c *Client) CheckRelogAccounts(ctx context.Context, emails []string, limit int) (RelogCheck, error) {
	payload := struct {
		Emails []string `json:"emails"`
		Limit  int      `json:"limit"`
	}{Emails: emails, Limit: limit}
	var response RelogCheck
	if err := c.postJSON(ctx, "/api/ops/accounts/batch", payload, &response); err != nil {
		return RelogCheck{}, err
	}
	if response.Items == nil {
		return RelogCheck{}, errors.New("SceneMint relog check response is missing items")
	}
	return response, nil
}

func (c *Client) postJSON(ctx context.Context, path string, payload, data any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode SceneMint response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		return fmt.Errorf("SceneMint request failed: status=%d code=%d message=%s", resp.StatusCode, envelope.Code, envelope.Message)
	}
	if data != nil && len(envelope.Data) > 0 {
		if err = json.Unmarshal(envelope.Data, data); err != nil {
			return fmt.Errorf("decode SceneMint data: %w", err)
		}
	}
	return nil
}
