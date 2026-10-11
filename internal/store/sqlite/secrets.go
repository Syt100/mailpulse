package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/security"
	"strings"
)

func (s *Store) BindKey(ctx context.Context, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("invalid master key")
	}
	s.key = append([]byte(nil), key...)
	p, e := s.Load(ctx, "system:keycheck")
	if e == nil {
		if !bytes.Equal(p, []byte("mailpulse-key-v1")) {
			return fmt.Errorf("invalid key check")
		}
		return nil
	}
	if e != domain.ErrNotFound {
		s.key = nil
		return e
	}
	var n int
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		s.key = nil
		return fmt.Errorf("missing key check on existing encrypted store")
	}
	return s.Save(ctx, "system:keycheck", []byte("mailpulse-key-v1"))
}
func (s *Store) Load(ctx context.Context, id string) ([]byte, error) {
	if len(s.key) != 32 {
		return nil, fmt.Errorf("master key not bound")
	}
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT data FROM secrets WHERE id=?`, id).Scan(&b)
	if e == sql.ErrNoRows {
		return nil, domain.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return security.Decrypt(s.key, b, []byte(id))
}
func (s *Store) Save(ctx context.Context, id string, p []byte) error {
	if len(s.key) != 32 {
		return fmt.Errorf("master key not bound")
	}
	b, e := security.Encrypt(s.key, p, []byte(id))
	if e != nil {
		return e
	}
	for _, prefix := range []string{"cache:", "identity:"} {
		if strings.HasPrefix(id, prefix) {
			res, err := s.db.ExecContext(ctx, `INSERT INTO secrets(id,data) SELECT ?,? WHERE EXISTS(SELECT 1 FROM accounts WHERE id=?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, id, b, strings.TrimPrefix(id, prefix))
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return domain.ErrNotFound
			}
			return nil
		}
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO secrets(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, id, b)
	return e
}

// SaveAuthorization saves a verified identity and its opaque MSAL cache as
// one transaction, checking account existence against concurrent removal.
func (s *Store) SaveAuthorization(ctx context.Context, id, identity string, cache []byte) error {
	values := map[string][]byte{"identity:" + id: []byte(identity), "cache:" + id: cache}
	for key, value := range values {
		b, e := security.Encrypt(s.key, value, []byte(key))
		if e != nil {
			return e
		}
		values[key] = b
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var exists int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=?`, id).Scan(&exists); e != nil {
		return e
	}
	if exists != 1 {
		return domain.ErrNotFound
	}
	for _, key := range []string{"identity:" + id, "cache:" + id} {
		if _, e = tx.ExecContext(ctx, `INSERT INTO secrets(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, key, values[key]); e != nil {
			return e
		}
	}
	return tx.Commit()
}
