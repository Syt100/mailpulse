package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Syt100/mailpulse/internal/domain"
)

func (s *Store) CreateAccount(ctx context.Context, a domain.Account, secret []byte) error {
	raw, e := json.Marshal(a)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO accounts(id,data) VALUES(?,?)`, a.ID, raw); e != nil {
		return e
	}
	if len(secret) > 0 {
		b, err := s.encrypt(a.ID, secret)
		if err != nil {
			return err
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO secrets(id,data) VALUES(?,?)`, a.ID, b); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	var a domain.Account
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT data FROM accounts WHERE id=?`, id).Scan(&b)
	if e == sql.ErrNoRows {
		return a, domain.ErrNotFound
	}
	if e != nil {
		return a, e
	}
	e = json.Unmarshal(b, &a)
	return a, e
}
func (s *Store) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT data FROM accounts ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Account{}
	for rows.Next() {
		var b []byte
		var a domain.Account
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	a, e := s.GetAccount(ctx, id)
	if e != nil {
		return e
	}
	a.Enabled = enabled
	b, _ := json.Marshal(a)
	_, e = s.db.ExecContext(ctx, `UPDATE accounts SET data=? WHERE id=?`, b, id)
	return e
}
func (s *Store) RemoveAccount(ctx context.Context, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM accounts WHERE id=?`, `DELETE FROM secrets WHERE id=?`, `DELETE FROM sync_state WHERE account_id=?`, `DELETE FROM processed_events WHERE account_id=?`, `DELETE FROM outbox WHERE account_id=?`, `DELETE FROM account_status WHERE account_id=?`} {
		if _, e = tx.ExecContext(ctx, q, id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) ResetState(ctx context.Context, id string) error {
	if _, e := s.GetAccount(ctx, id); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM sync_state WHERE account_id=?`, `DELETE FROM processed_events WHERE account_id=?`, `DELETE FROM account_status WHERE account_id=?`} {
		if _, e = tx.ExecContext(ctx, q, id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) ListRules(ctx context.Context) ([]domain.Rule, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT data FROM rules ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	rs := []domain.Rule{}
	for rows.Next() {
		var b []byte
		var r domain.Rule
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}
func (s *Store) AddRule(ctx context.Context, r domain.Rule) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO rules(id,data) VALUES(?,?)`, r.ID, b)
	return e
}
func (s *Store) RemoveRule(ctx context.Context, id string) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id=?`, id)
	return e
}
