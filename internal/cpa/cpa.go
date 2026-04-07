package cpa

import (
	"context"
	"errors"
	"strings"

	"github.com/sunls24/gox/network/client"
	"github.com/sunls24/gox/network/header"
	"github.com/tidwall/gjson"
)

type Client struct {
	baseURL, token string
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
	}
}

func (c *Client) CodexAuthURL(ctx context.Context) (string, error) {
	const PATH = "/v0/management/codex-auth-url?is_webui=true"
	body, err := client.Get(ctx, c.baseURL+PATH, header.New().Authorization(c.token).Get()...)
	if err != nil {
		return "", err
	}
	if gjson.GetBytes(body, "status").String() != "ok" {
		return "", errors.New(string(body))
	}
	return gjson.GetBytes(body, "url").String(), nil
}

func (c *Client) CodexCallback(ctx context.Context, redirectURL string) error {
	return c.OauthCallback(ctx, "codex", redirectURL)
}

func (c *Client) OauthCallback(ctx context.Context, provider, redirectURL string) error {
	const PATH = "/v0/management/oauth-callback"
	body, err := client.Post(ctx, c.baseURL+PATH, map[string]string{
		"provider":     provider,
		"redirect_url": redirectURL,
	}, header.New().Authorization(c.token).Get()...)
	if err != nil {
		return err
	}
	if gjson.GetBytes(body, "status").String() != "ok" {
		return errors.New(string(body))
	}
	return nil
}
