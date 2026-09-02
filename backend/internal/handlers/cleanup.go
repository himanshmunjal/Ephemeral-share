package handlers

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/storage"
	"ephemeral-share/internal/ws"
)

// CleanupWorker listens for Redis key-expiry events on `session:*` keys and
// performs cross-store cleanup (S3 objects + WebSocket disconnects). It also
// runs a periodic backstop sweep in case a notification is ever missed
// (e.g. Redis restart), guaranteeing at-least-once cleanup.
type CleanupWorker struct {
	rdb   *redis.Client
	s3    *storage.Client
	stats *analytics.Store // nil-safe: analytics is best-effort, never required
}

func NewCleanupWorker(rdb *redis.Client, s3 *storage.Client, stats *analytics.Store) *CleanupWorker {
	return &CleanupWorker{rdb: rdb, s3: s3, stats: stats}
}

// Run requires `notify-keyspace-events Ex` to be enabled on the Redis server
// (see docker-compose.yml command flags).
func (w *CleanupWorker) Run(ctx context.Context) {
	pubsub := w.rdb.PSubscribe(ctx, "__keyevent@0__:expired")
	defer pubsub.Close()

	ch := pubsub.Channel()
	log.Println("cleanup worker: listening for session key expirations")

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Only the primary "session:{code}" hash key (not the sub-keys,
			// which expire at effectively the same moment) triggers cleanup,
			// to avoid redundant duplicate work.
			if !strings.HasPrefix(msg.Payload, "session:") || strings.Count(msg.Payload, ":") != 1 {
				continue
			}
			code := strings.TrimPrefix(msg.Payload, "session:")
			w.cleanupSession(ctx, code)
		}
	}
}

func (w *CleanupWorker) cleanupSession(ctx context.Context, code string) {
	log.Printf("cleaning up expired session %s", code)
	if err := w.s3.DeleteSessionObjects(ctx, code); err != nil {
		log.Printf("cleanup: failed to delete S3 objects for %s: %v", code, err)
	}
	if w.stats != nil {
		w.stats.MarkSessionEnded(code)
	}
	ws.NotifyExpired(code)
	// Any leftover Redis sub-keys (content/participants) share the same TTL
	// and will already be gone or gone within the same instant; no further
	// Redis action needed here.
}

// PeriodicSweep is a defense-in-depth pass that catches anything the
// keyspace-notification path might have missed, e.g. after a Redis restart
// where notifications were briefly unavailable.
func (w *CleanupWorker) PeriodicSweep(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.stats == nil {
				continue
			}
			n, err := w.stats.SweepExpiredSessions(ctx)
			if err != nil {
				log.Printf("cleanup: periodic sweep failed: %v", err)
			} else if n > 0 {
				log.Printf("cleanup: periodic sweep marked %d stale session(s) ended", n)
			}
		}
	}
}
