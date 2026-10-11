package graph

import (
	"context"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/Syt100/mailpulse/internal/domain"
)

type CacheAdapter struct {
	Secrets   domain.SecretStore
	AccountID string
}

func (a *CacheAdapter) Replace(ctx context.Context, u cache.Unmarshaler, h cache.ReplaceHints) error {
	b, e := a.Secrets.Load(ctx, "cache:"+a.AccountID)
	if e == domain.ErrNotFound {
		return nil
	}
	if e != nil {
		return e
	}
	if len(b) == 0 {
		return nil
	}
	return u.Unmarshal(b)
}
func (a *CacheAdapter) Export(ctx context.Context, m cache.Marshaler, h cache.ExportHints) error {
	b, e := m.Marshal()
	if e != nil {
		return e
	}
	return a.Secrets.Save(ctx, "cache:"+a.AccountID, b)
}

type stagingCache struct{ raw []byte }

func (a *stagingCache) Replace(ctx context.Context, u cache.Unmarshaler, h cache.ReplaceHints) error {
	if len(a.raw) == 0 {
		return nil
	}
	return u.Unmarshal(a.raw)
}
func (a *stagingCache) Export(ctx context.Context, m cache.Marshaler, h cache.ExportHints) error {
	b, e := m.Marshal()
	if e == nil {
		a.raw = append([]byte(nil), b...)
	}
	return e
}
