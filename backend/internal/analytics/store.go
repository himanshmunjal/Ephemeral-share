// Package analytics tracks aggregate traffic to Postgres: how many sessions
// were created, whether they're still active, and how many text/file shares
// happened in each — never the shared content itself, which stays in Redis
// only and is gone the moment a session expires.
package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ephemeral-share/internal/models"
)

// Store wraps a Postgres connection pool for the analytics tables. All writes
// are best-effort and fire in the background: analytics must never slow down
// or fail a user-facing request whose real state lives in Redis.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pooled connection to Postgres. The pool is sized for a modest
// write-behind workload (a handful of small writes per session) rather than
// the request-serving hot path, which stays entirely on Redis.
func New(ctx context.Context, dsn string) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	poolCfg.MaxConns = 10
	poolCfg.MinConns = 1
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 10 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// run executes a write in the background so a slow/unavailable Postgres
// never adds latency to (or fails) the caller's Redis-backed request.
func (s *Store) run(name string, fn func(ctx context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			log.Printf("analytics: %s failed: %v", name, err)
		}
	}()
}

// RecordSessionCreated inserts the analytics row for a newly created session.
// Codes are recycled once a session expires (GenerateUniqueCode only checks
// for currently-live codes in Redis), so code_hash is intentionally NOT a
// dedupe key here — every creation gets its own historical row.
func (s *Store) RecordSessionCreated(sess models.Session) {
	s.run("record session created", func(ctx context.Context) error {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO sessions (code_hash, type, max_participants, created_at, expires_at, session_token_hash, is_active)
			VALUES ($1, $2, $3, $4, $5, $6, true)`,
			hashToken(sess.Code), sess.Type, sess.MaxParticipants, sess.CreatedAt, sess.ExpiresAt, hashToken(sess.OwnerToken),
		)
		return err
	})
}

// IncrementTextShared bumps the text-share counter on the session's current
// (active) row — a stale, since-expired row sharing the same recycled code
// must never be touched.
func (s *Store) IncrementTextShared(code string) {
	s.run("increment text shared", func(ctx context.Context) error {
		_, err := s.pool.Exec(ctx,
			`UPDATE sessions SET text_shared_count = text_shared_count + 1 WHERE code_hash = $1 AND is_active = true`,
			hashToken(code),
		)
		return err
	})
}

// IncrementFileShared bumps the file-share counter on the session's current (active) row.
func (s *Store) IncrementFileShared(code string) {
	s.run("increment file shared", func(ctx context.Context) error {
		_, err := s.pool.Exec(ctx,
			`UPDATE sessions SET file_shared_count = file_shared_count + 1 WHERE code_hash = $1 AND is_active = true`,
			hashToken(code),
		)
		return err
	})
}

// MarkSessionEnded flips is_active to false, whether the session was
// terminated early by its owner or simply expired.
func (s *Store) MarkSessionEnded(code string) {
	s.run("mark session ended", func(ctx context.Context) error {
		_, err := s.pool.Exec(ctx,
			`UPDATE sessions SET is_active = false, expired_at = now() WHERE code_hash = $1 AND is_active = true`,
			hashToken(code),
		)
		return err
	})
}

// SweepExpiredSessions flips is_active to false for any row whose expires_at
// has passed but which never got a MarkSessionEnded call (e.g. a missed
// Redis keyspace-expiry notification). Backstop only: the Redis-driven path
// in CleanupWorker.cleanupSession is what normally marks sessions ended.
func (s *Store) SweepExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET is_active = false, expired_at = now() WHERE is_active = true AND expires_at < now()`,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
