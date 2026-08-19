package scenemint

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

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

const requestTimeout = 5 * time.Minute

type Account struct {
	Email          string    `json:"email"`
	TokenExpiresAt time.Time `json:"tokenExpiresAt"`
	Status         string    `json:"status"`
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
	body, err := json.Marshal(map[string]string{"accessToken": accessToken})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/ops/accounts", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

func (c *Client) GetByEmail(ctx context.Context, email string) (*Account, error) {
	endpoint, err := url.Parse(c.baseURL + "/api/ops/accounts")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("email", email)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	var response struct {
		Items []Account `json:"items"`
	}
	if err = c.do(req, &response); err != nil {
		return nil, err
	}
	if len(response.Items) == 0 {
		return nil, nil
	}
	return &response.Items[0], nil
}

func (c *Client) do(req *http.Request, data any) error {
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
