package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type TokenSource interface {
	AccessToken(context.Context, string) (string, error)
}
type Provider struct {
	tokens  TokenSource
	http    *http.Client
	baseURL string
}

func NewProvider(tokens TokenSource, client *http.Client) *Provider {
	var h http.Client
	if client != nil {
		h = *client
	}
	if h.Timeout == 0 {
		h.Timeout = 30 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{tokens, &h, "https://graph.microsoft.com"}
}
func (p *Provider) validate(link string) error {
	u, e := url.Parse(link)
	b, _ := url.Parse(p.baseURL)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !(u.Host == b.Host || u.Host == "graph.microsoft.com") {
		return fmt.Errorf("untrusted Graph link")
	}
	return nil
}
func (p *Provider) Scan(ctx context.Context, a domain.Account, c domain.Cursor) (domain.Batch, error) {
	link := c.Position
	if link == "" {
		link = p.baseURL + `/v1.0/me/mailFolders/inbox/messages/delta?changeType=created&$select=id,subject,from,receivedDateTime,webLink&$top=100`
	}
	if e := p.validate(link); e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "needs_resync"}
	}
	token, e := p.tokens.AccessToken(ctx, a.ID)
	if e != nil {
		return domain.Batch{}, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", link, nil)
	if e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Prefer", `IdType="ImmutableId"`)
	res, e := p.http.Do(req)
	if e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		code := "transient"
		switch res.StatusCode {
		case 401, 403:
			code = "authentication_required"
		case 410:
			code = "needs_resync"
		case 429:
			code = "throttled"
		}
		var after time.Duration
		if n, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && n > 0 {
			after = time.Duration(n) * time.Second
		}
		return domain.Batch{}, &domain.ProviderError{Code: code, RetryAfter: after}
	}
	var data struct {
		Value []struct {
			ID      string  `json:"id"`
			Subject *string `json:"subject"`
			From    *struct {
				EmailAddress struct{ Name, Address string } `json:"emailAddress"`
			} `json:"from"`
			ReceivedDateTime string          `json:"receivedDateTime"`
			WebLink          string          `json:"webLink"`
			Removed          json.RawMessage `json:"@removed"`
		} `json:"value"`
		Next  string `json:"@odata.nextLink"`
		Delta string `json:"@odata.deltaLink"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&data); e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	nextLink := data.Next
	more := nextLink != ""
	if nextLink == "" {
		nextLink = data.Delta
	}
	if nextLink == "" {
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	if e = p.validate(nextLink); e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "needs_resync"}
	}
	b := domain.Batch{Next: domain.Cursor{AccountID: a.ID, Folder: "INBOX", Position: nextLink, Initialized: c.Initialized || !more}, Baseline: !c.Initialized, More: more}
	for _, m := range data.Value {
		if m.ID == "" || len(m.Removed) > 0 {
			continue
		}
		if c.Initialized && m.Subject == nil && m.From == nil && m.ReceivedDateTime == "" && m.WebLink == "" {
			continue
		}
		ev := domain.MailEvent{Key: a.ID + ":graph:" + m.ID, AccountID: a.ID, Folder: "INBOX", Kind: "added", ClickURL: m.WebLink}
		if m.Subject != nil {
			ev.Subject = domain.Clean(*m.Subject, 500)
		}
		if m.From != nil {
			ev.SenderAddress = domain.Clean(m.From.EmailAddress.Address, 200)
			ev.SenderName = domain.Clean(m.From.EmailAddress.Name, 100)
		}
		ev.ReceivedAt, _ = time.Parse(time.RFC3339, m.ReceivedDateTime)
		b.Events = append(b.Events, ev)
	}
	return b, nil
}
func (p *Provider) Wait(ctx context.Context, a domain.Account, interval time.Duration) error {
	t := time.NewTimer(interval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
