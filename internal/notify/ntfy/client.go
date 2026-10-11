package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config = domain.NotificationConfig
type Client struct {
	baseURL, topic string
	token          func(context.Context) (string, error)
	http           *http.Client
}

func NewClient(baseURL, topic string, token func(context.Context) (string, error), client *http.Client) (*Client, error) {
	if err := (domain.NotificationConfig{URL: baseURL, Topic: topic}).Validate(); err != nil {
		return nil, err
	}
	var h http.Client
	if client != nil {
		h = *client
	}
	if h.Timeout == 0 {
		h.Timeout = 20 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{strings.TrimRight(baseURL, "/") + "/", topic, token, &h}, nil
}
func (c *Client) Publish(ctx context.Context, n domain.Notification) error {
	body := struct {
		domain.Notification
		Topic string `json:"topic"`
	}{n, c.topic}
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	if len(raw) > 4096 {
		return &domain.DeliveryError{Status: "failed", Code: "message_too_large"}
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(raw))
	if e != nil {
		return &domain.DeliveryError{Status: "failed", Code: "invalid_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != nil {
		token, err := c.token(ctx)
		if err != nil {
			return &domain.DeliveryError{Status: "blocked", Code: "ntfy_token_unavailable"}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	res, e := c.http.Do(req)
	if e != nil {
		return &domain.DeliveryError{Status: "pending", Code: "network_error"}
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 32768))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	status := "failed"
	switch {
	case res.StatusCode == 401 || res.StatusCode == 403:
		status = "blocked"
	case res.StatusCode == 429 || res.StatusCode >= 500:
		status = "pending"
	}
	after := time.Duration(0)
	value := res.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		after = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(value); err == nil {
		after = time.Until(until)
		if after < 0 {
			after = 0
		}
	}
	return &domain.DeliveryError{Status: status, Code: fmt.Sprintf("ntfy_http_%d", res.StatusCode), RetryAfter: after}
}

type Dynamic struct {
	Secrets domain.SecretStore
	HTTP    *http.Client
}

func (d *Dynamic) Publish(ctx context.Context, n domain.Notification) error {
	raw, e := d.Secrets.Load(ctx, "system:ntfy")
	if e != nil {
		return &domain.DeliveryError{Status: "blocked", Code: "ntfy_not_configured"}
	}
	var cfg Config
	if json.Unmarshal(raw, &cfg) != nil {
		return &domain.DeliveryError{Status: "blocked", Code: "ntfy_configuration_invalid"}
	}
	c, e := NewClient(cfg.URL, cfg.Topic, func(context.Context) (string, error) { return cfg.Token, nil }, d.HTTP)
	if e != nil {
		return &domain.DeliveryError{Status: "blocked", Code: "ntfy_configuration_invalid"}
	}
	return c.Publish(ctx, n)
}
