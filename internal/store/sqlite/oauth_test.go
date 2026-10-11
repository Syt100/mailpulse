package sqlite

import (
	"context"
	"testing"

	"github.com/Syt100/mailpulse/internal/domain"
)

func TestOAuthPersistenceCannotResurrectRemovedAccount(t *testing.T) {
	s := openTest(t, 10)
	ctx := context.Background()
	if e := s.BindKey(ctx, make([]byte, 32)); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateAccount(ctx, domain.Account{ID: "a"}, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveAccount(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveAuthorization(ctx, "a", "home", []byte("cache")); e == nil {
		t.Fatal("authorization resurrected removed credentials")
	}
	if e := s.Save(ctx, "cache:a", []byte("refreshed")); e == nil {
		t.Fatal("refresh resurrected removed cache")
	}
	for _, id := range []string{"identity:a", "cache:a"} {
		if _, e := s.Load(ctx, id); e != domain.ErrNotFound {
			t.Fatalf("secret survived: %s %v", id, e)
		}
	}
}

func TestOAuthIdentityAndCacheCommitAtomically(t *testing.T) {
	s := openTest(t, 10)
	ctx := context.Background()
	if e := s.BindKey(ctx, make([]byte, 32)); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateAccount(ctx, domain.Account{ID: "a"}, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveAuthorization(ctx, "a", "original", []byte("old-cache")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(`CREATE TRIGGER reject_oauth_cache BEFORE INSERT ON secrets WHEN NEW.id='cache:a' BEGIN SELECT RAISE(FAIL,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveAuthorization(ctx, "a", "replacement", []byte("new-cache")); e == nil {
		t.Fatal("injected storage failure hidden")
	}
	identity, e := s.Load(ctx, "identity:a")
	if e != nil || string(identity) != "original" {
		t.Fatal("identity partially replaced")
	}
	cache, e := s.Load(ctx, "cache:a")
	if e != nil || string(cache) != "old-cache" {
		t.Fatal("cache partially replaced")
	}
}
