package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/Syt100/mailpulse/internal/domain"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	db       *sql.DB
	capacity int
	key      []byte
}

func NewID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func Open(ctx context.Context, path string, capacity int) (*Store, error) {
	if capacity < 1 {
		capacity = 10000
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, capacity: capacity}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err = os.Chmod(path, 0600); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY);INSERT OR IGNORE INTO schema_migrations VALUES(1);
CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS rules(id TEXT PRIMARY KEY,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS secrets(id TEXT PRIMARY KEY,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS sync_state(account_id TEXT NOT NULL,folder TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(account_id,folder));
CREATE TABLE IF NOT EXISTS processed_events(account_id TEXT NOT NULL,event_key TEXT NOT NULL,PRIMARY KEY(account_id,event_key));
CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,account_id TEXT NOT NULL,event_key TEXT NOT NULL,payload BLOB NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempt INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,lease_until INTEGER NOT NULL DEFAULT 0,sent_at INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',UNIQUE(account_id,event_key));
CREATE INDEX IF NOT EXISTS outbox_due ON outbox(status,next_at,lease_until);
CREATE TABLE IF NOT EXISTS account_status(account_id TEXT PRIMARY KEY,data BLOB NOT NULL);
`)
	return err
}
func (s *Store) Cursor(ctx context.Context, account, folder string) (domain.Cursor, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sync_state WHERE account_id=? AND folder=?`, account, folder).Scan(&b)
	if err == sql.ErrNoRows {
		return domain.Cursor{AccountID: account, Folder: folder}, nil
	}
	if err != nil {
		return domain.Cursor{}, err
	}
	var c domain.Cursor
	err = json.Unmarshal(b, &c)
	return c, err
}
func (s *Store) CommitBatch(ctx context.Context, b domain.Batch, ds []domain.Decision) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	decisions := map[string]domain.Decision{}
	for _, d := range ds {
		decisions[d.EventKey] = d
	}
	for _, e := range b.Events {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO processed_events(account_id,event_key) VALUES(?,?)`, b.Next.AccountID, e.Key)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		d := decisions[e.Key]
		if n == 0 || !d.Notify || b.Baseline {
			continue
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE status!='sent'`).Scan(&count); err != nil {
			return err
		}
		if count >= s.capacity {
			return domain.ErrCapacity
		}
		payload, err := json.Marshal(d.Payload)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox(id,account_id,event_key,payload) VALUES(?,?,?,?)`, NewID(), b.Next.AccountID, e.Key, payload); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(b.Next)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sync_state(account_id,folder,data) VALUES(?,?,?) ON CONFLICT(account_id,folder) DO UPDATE SET data=excluded.data`, b.Next.AccountID, b.Next.Folder, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ClaimDeliveries(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.Delivery, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,account_id,event_key,payload,attempt,next_at FROM outbox WHERE status='pending' AND next_at<=? AND lease_until<=? ORDER BY next_at,id LIMIT ?`, now.Unix(), now.Unix(), limit)
	if e != nil {
		return nil, e
	}
	var jobs []domain.Delivery
	for rows.Next() {
		var d domain.Delivery
		var raw []byte
		var next int64
		if e = rows.Scan(&d.ID, &d.AccountID, &d.EventKey, &raw, &d.Attempt, &next); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal(raw, &d.Payload); e != nil {
			rows.Close()
			return nil, e
		}
		d.NextAttemptAt = time.Unix(next, 0)
		d.LeaseUntil = now.Add(lease)
		jobs = append(jobs, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, d := range jobs {
		if _, e = tx.ExecContext(ctx, `UPDATE outbox SET lease_until=? WHERE id=?`, d.LeaseUntil.Unix(), d.ID); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return jobs, nil
}
