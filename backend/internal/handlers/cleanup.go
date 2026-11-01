package handlers

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/redisclient"
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
	store *redisclient.Store
}

func NewCleanupWorker(rdb *redis.Client, s3 *storage.Client, stats *analytics.Store, store *redisclient.Store) *CleanupWorker {
	return &CleanupWorker{rdb: rdb, s3: s3, stats: stats, store: store}
}

// OrphanSweep deletes storage objects whose session no longer exists in
// Redis. Keyspace notifications are fire-and-forget: if the backend was down
// or restarting when a session expired, its files would otherwise live
// forever and eat the free storage quota. Objects younger than `grace` are
// skipped so in-flight uploads for brand-new sessions are never touched.
func (w *CleanupWorker) OrphanSweep(ctx context.Context, interval, grace time.Duration) {
	w.SweepOrphansOnce(ctx, grace) // catch anything left over from before this boot
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.SweepOrphansOnce(ctx, grace)
		}
	}
}

// SweepOrphansOnce runs one orphan pass and returns how many objects it
// deleted. Exposed separately so it can be tested without timers.
func (w *CleanupWorker) SweepOrphansOnce(ctx context.Context, grace time.Duration) int {
	objs, err := w.s3.ListSessionObjects(ctx)
	if err != nil {
		log.Printf("orphan sweep: list failed: %v", err)
		return 0
	}
	live := map[string]bool{}
	deleted := 0
	for _, o := range objs {
		if time.Since(o.LastModified) < grace {
			continue
		}
		alive, seen := live[o.SessionCode]
		if !seen {
			alive, err = w.store.SessionExists(ctx, o.SessionCode)
			if err != nil {
				continue // Redis hiccup: skip rather than delete live data
			}
			live[o.SessionCode] = alive
		}
		if alive {
			continue
		}
		if err := w.s3.DeleteObject(ctx, o.Key); err != nil {
			log.Printf("orphan sweep: delete failed: %v", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		log.Printf("orphan sweep: removed %d orphaned object(s)", deleted)
	}
	return deleted
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
