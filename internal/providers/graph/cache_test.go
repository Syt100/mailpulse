package graph

import (
	"context"
	"errors"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"testing"
)

type secrets struct {
	data map[string][]byte
	fail bool
}

func (s *secrets) Load(ctx context.Context, id string) ([]byte, error) { return s.data[id], nil }
func (s *secrets) Save(ctx context.Context, id string, b []byte) error {
	if s.fail {
		return errors.New("disk failure")
	}
	s.data[id] = append([]byte(nil), b...)
	return nil
}

type serial struct{ data []byte }

func (s *serial) Marshal() ([]byte, error) { return s.data, nil }
func (s *serial) Unmarshal(b []byte) error { s.data = append([]byte(nil), b...); return nil }
func TestTwoAccountCacheIsolation(t *testing.T) {
	s := &secrets{data: map[string][]byte{}}
	a := &CacheAdapter{Secrets: s, AccountID: "a"}
	b := &CacheAdapter{Secrets: s, AccountID: "b"}
	a.Export(context.Background(), &serial{[]byte("A")}, cache.ExportHints{})
	b.Export(context.Background(), &serial{[]byte("B")}, cache.ExportHints{})
	dst := &serial{}
	a.Replace(context.Background(), dst, cache.ReplaceHints{})
	if string(dst.data) != "A" {
		t.Fatal("mixed account caches")
	}
}
func TestRefreshPersistsBeforeUse(t *testing.T) {
	s := &secrets{data: map[string][]byte{}, fail: true}
	a := &CacheAdapter{Secrets: s, AccountID: "a"}
	if e := a.Export(context.Background(), &serial{[]byte("token")}, cache.ExportHints{}); e == nil {
		t.Fatal("cache failure hidden")
	}
}
