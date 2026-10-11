package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"time"
)

type OutboxItem = domain.OutboxItem

func (s *Store) ListOutbox(ctx context.Context) ([]OutboxItem, error) {
	page, err := s.ListOutboxPage(ctx, domain.OutboxQuery{Limit: 1000})
	return page.Items, err
}

func (s *Store) ListOutboxPage(ctx context.Context, q domain.OutboxQuery) (domain.OutboxPage, error) {
	page := domain.OutboxPage{Items: []domain.OutboxItem{}}
	if q.Limit < 1 || q.Limit > 1000 {
		return page, fmt.Errorf("invalid page size")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,account_id,status,attempt,error_code FROM outbox WHERE id>? AND (?='' OR status=?) AND (?='' OR account_id=?) ORDER BY id LIMIT ?`, q.After, q.Status, q.Status, q.AccountID, q.AccountID, q.Limit+1)
	if e != nil {
		return page, e
	}
	defer rows.Close()
	for rows.Next() {
		var x OutboxItem
		if e = rows.Scan(&x.ID, &x.AccountID, &x.Status, &x.Attempt, &x.ErrorCode); e != nil {
			return page, e
		}
		page.Items = append(page.Items, x)
	}
	if e = rows.Err(); e != nil {
		return page, e
	}
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

// One transaction keeps per-account and global queue counts in the same snapshot.
func (s *Store) NotificationStatus(ctx context.Context) (domain.RuntimeStatus, error) {
	status := domain.RuntimeStatus{Accounts: []domain.AccountStatus{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return status, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.data,st.data,COALESCE(q.n,0) FROM accounts a LEFT JOIN account_status st ON st.account_id=a.id LEFT JOIN (SELECT account_id,count(*) n FROM outbox WHERE status!='sent' GROUP BY account_id) q ON q.account_id=a.id ORDER BY a.id`)
	if err != nil {
		return status, err
	}
	for rows.Next() {
		var id string
		var rawAccount, rawStatus []byte
		var count int
		if err = rows.Scan(&id, &rawAccount, &rawStatus, &count); err != nil {
			rows.Close()
			return status, err
		}
		var a domain.Account
		if err = json.Unmarshal(rawAccount, &a); err != nil {
			rows.Close()
			return status, err
		}
		item := domain.AccountStatus{AccountID: id, State: "initializing"}
		if len(rawStatus) > 0 {
			if err = json.Unmarshal(rawStatus, &item); err != nil {
				rows.Close()
				return status, err
			}
		}
		item.AccountID = id
		if !a.Enabled {
			item.State = "paused"
		}
		item.PendingNotifications = count
		status.Accounts = append(status.Accounts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return status, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE status!='sent'`).Scan(&status.PendingNotifications); err != nil {
		return status, err
	}
	return status, tx.Commit()
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

// Release only this claim; a cancelled or expired sender cannot release a newer one.
func (s *Store) ReleaseDelivery(ctx context.Context, id, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET lease_until=0,lease_token='' WHERE id=? AND lease_token=? AND status='pending'`, id, token)
	return err
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
