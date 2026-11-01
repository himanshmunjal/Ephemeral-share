package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/config"
	"ephemeral-share/internal/handlers"
	"ephemeral-share/internal/monitoring"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on environment variables")
	}

	cfg := config.Load()
	ctx := context.Background()

	// Structured JSON logs in production (Railway/Render/Loki parse these);
	// readable text locally.
	logHandler := slog.Default().Handler()
	if gin.Mode() == gin.ReleaseMode {
		logHandler = slog.NewJSONHandler(os.Stdout, nil)
	}
	// Optional Sentry (free tier): Error-level logs and panics are reported
	// with session codes scrubbed. Off unless SENTRY_DSN is set.
	sentryOn, flushSentry := monitoring.Init(cfg.SentryDSN, cfg.SentryEnv)
	defer flushSentry()
	if sentryOn {
		logHandler = monitoring.Wrap(logHandler)
		log.Println("error tracking: Sentry enabled")
	}
	slog.SetDefault(slog.New(logHandler))

	// GIN_MODE=release is the standard signal that this is a real deployment,
	// not a docker-compose/local-dev run — use it to refuse to boot with
	// leftover dev secrets or a localhost-only CORS/S3 setup.
	if gin.Mode() == gin.ReleaseMode {
		if err := cfg.CheckProductionSafety(); err != nil {
			log.Fatal(err)
		}
	}

	rdb := redis.NewClient(redisclient.Options(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisTLS))
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis connection failed: %v", err)
	}
	// The cleanup worker (and therefore the entire TTL/expiry guarantee) is
	// driven entirely by Redis keyspace notifications, so verify the server
	// actually has them turned on rather than silently never cleaning up.
	// Some managed Redis tiers (notably serverless offerings) block CONFIG SET
	// for this and must have it enabled out-of-band before deploy.
	if err := verifyKeyspaceNotifications(ctx, rdb); err != nil {
		// Hosts that forbid CONFIG SET (some serverless Redis) can opt out:
		// the orphan sweep still deletes expired sessions' files every few
		// minutes and clients' countdowns still end the session — only the
		// instant server-side socket/file teardown is lost.
		if os.Getenv("ALLOW_NO_KEYSPACE_EVENTS") == "true" {
			log.Printf("WARNING: %v — continuing; relying on the orphan sweep for cleanup", err)
		} else {
			log.Fatalf("redis keyspace notifications check failed: %v", err)
		}
	}
	store := redisclient.New(cfg.RedisAddr, cfg.RedisPassword, cfg.SessionTTL, cfg.RedisTLS)

	s3Client, err := storage.New(ctx, cfg.S3Endpoint, cfg.S3PublicEndpoint, cfg.S3Region, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		log.Fatalf("s3 client init failed: %v", err)
	}

	// Traffic analytics (session counts, active/inactive, text/file share
	// counts) live in Postgres, separate from Redis' live session state.
	// Non-fatal if unavailable: the product still works end-to-end on Redis
	// alone, it just temporarily loses traffic tracking.
	var statsStore *analytics.Store
	if cfg.PostgresDSN == "" {
		log.Println("analytics: POSTGRES_DSN not set, traffic tracking disabled (Redis-only mode)")
	} else {
		statsStore, err = analytics.New(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Printf("analytics: postgres unavailable, traffic tracking disabled: %v", err)
			statsStore = nil
		} else {
			defer statsStore.Close()
		}
	}

	// Background cleanup: reacts to Redis key-expiry events for guaranteed
	// teardown of S3 objects and WebSocket connections when a session's TTL hits zero.
	cleanupWorker := handlers.NewCleanupWorker(rdb, s3Client, statsStore, store)
	go cleanupWorker.Run(ctx)
	go cleanupWorker.PeriodicSweep(ctx, time.Minute)
	go cleanupWorker.OrphanSweep(ctx, cfg.OrphanSweepInterval, cfg.OrphanGrace)

	router := handlers.NewRouter(store, s3Client, cfg, statsStore)
	log.Printf("ephemeral-share backend listening on :%s", cfg.Port)
	if err := router.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

// verifyKeyspaceNotifications confirms the Redis server has "Ex" (or a
// superset, e.g. "KEA") set for notify-keyspace-events. The cleanup worker's
// PSubscribe silently receives nothing if this isn't set — sessions would
// never have their S3 objects or WebSocket connections cleaned up, with no
// error anywhere, so this must be checked eagerly at boot instead.
func verifyKeyspaceNotifications(ctx context.Context, rdb *redis.Client) error {
	// Best effort: many managed Redis instances (Railway's included) allow
	// CONFIG SET, so enable it ourselves instead of requiring manual setup.
	// "KEA" is a superset of "Ex"; ignore failure — the check below decides.
	_ = rdb.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err()

	res, err := rdb.ConfigGet(ctx, "notify-keyspace-events").Result()
	if err != nil {
		// Managed Redis (e.g. some serverless tiers) can disallow CONFIG GET
		// entirely; treat that as "can't verify" rather than a hard failure,
		// but make it loud so it isn't missed.
		log.Printf("WARNING: could not read notify-keyspace-events from Redis (%v) — "+
			"confirm keyspace notifications for expired keys are enabled out-of-band, "+
			"the cleanup worker depends on it entirely", err)
		return nil
	}
	flags := res["notify-keyspace-events"]
	hasExpired := strings.ContainsAny(flags, "A") || (strings.ContainsAny(flags, "gx") && strings.Contains(flags, "E"))
	if !hasExpired {
		return fmt.Errorf(
			"notify-keyspace-events is %q, which does not include expired-key keyevent notifications (\"Ex\") — "+
				"the cleanup worker will never fire and expired sessions' S3 objects/WebSocket connections will leak. "+
				"Set it via redis.conf, CONFIG SET notify-keyspace-events Ex, or your managed provider's console "+
				"(verify it supports this before choosing it — see README)", flags)
	}
	return nil
}
