package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"time"
)

type OutboxItem struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Status    string `json:"status"`
	Attempt   int    `json:"attempt"`
	ErrorCode string `json:"error_code,omitempty"`
}

func (s *Store) ListOutbox(ctx context.Context) ([]OutboxItem, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,account_id,status,attempt,error_code FROM outbox ORDER BY id LIMIT 1000`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []OutboxItem{}
	for rows.Next() {
		var x OutboxItem
		if e = rows.Scan(&x.ID, &x.AccountID, &x.Status, &x.Attempt, &x.ErrorCode); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE status!='sent'`).Scan(&n)
	return n, e
}
func (s *Store) MarkSent(ctx context.Context, id, token string, now time.Time) error {
	_, e := s.db.ExecContext(ctx, `UPDATE outbox SET status='sent',lease_until=0,lease_token='',sent_at=?,error_code='' WHERE id=? AND lease_token=? AND status='pending'`, now.Unix(), id, token)
	return e
}
func (s *Store) MarkFailure(ctx context.Context, id, token, status, code string, next time.Time) error {
	_, e := s.db.ExecContext(ctx, `UPDATE outbox SET status=?,attempt=attempt+1,lease_until=0,lease_token='',next_at=?,error_code=? WHERE id=? AND lease_token=? AND status='pending'`, status, next.Unix(), code, id, token)
	return e
}
func (s *Store) Retry(ctx context.Context, id string) error {
	res, e := s.db.ExecContext(ctx, `UPDATE outbox SET status='pending',next_at=0,lease_until=0,lease_token='',error_code='' WHERE id=? AND status!='sent' AND lease_until<=?`, id, time.Now().Unix())
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return fmt.Errorf("job absent, sent or currently leased")
	}
	return nil
}
func (s *Store) Unblock(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE outbox SET status='pending',next_at=0,error_code='' WHERE status='blocked'`)
	return e
}
func (s *Store) Prune(ctx context.Context, now time.Time) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE status='sent' AND sent_at<=?`, now.Add(-7*24*time.Hour).Unix())
	return e
}
func (s *Store) PutStatus(ctx context.Context, status domain.AccountStatus) error {
	b, e := json.Marshal(status)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO account_status(account_id,data) VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET data=excluded.data`, status.AccountID, b)
	return e
}
func (s *Store) Statuses(ctx context.Context) ([]domain.AccountStatus, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT data FROM account_status ORDER BY account_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.AccountStatus{}
	for rows.Next() {
		var b []byte
		var status domain.AccountStatus
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &status); e != nil {
			return nil, e
		}
		out = append(out, status)
	}
	return out, rows.Err()
}
