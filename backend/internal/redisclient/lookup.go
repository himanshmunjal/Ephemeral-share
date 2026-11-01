package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/models"
)

// LookupOpts selects the extra data a request needs alongside the session.
type LookupOpts struct {
	Presence   bool // also count live connections
	ContentIDs bool // also fetch the ordered content ids
}

// Lookup is everything a request usually needs to know up front, fetched in a
// single round trip.
type Lookup struct {
	Session    *models.Session // nil if the code is unknown/expired
	TTL        time.Duration   // session time remaining
	Online     int
	ContentIDs []string
	Failures   int           // wrong-code misses recorded for this IP
	LockTTL    time.Duration // how long the IP stays locked once over the limit
}

// Lookup pipelines the lockout counter, the session record and its TTL (plus
// optional presence/content ids) into ONE round trip. Against a remote Redis
// each round trip costs real time, and these used to be four separate calls
// on every request.
func (s *Store) Lookup(ctx context.Context, ip, code string, opts LookupOpts) (*Lookup, error) {
	pipe := s.rdb.Pipeline()
	bf := pipe.Get(ctx, lockKey(ip))
	bfTTL := pipe.TTL(ctx, lockKey(ip))
	sess := pipe.Get(ctx, sessionKey(code))
	pttl := pipe.PTTL(ctx, sessionKey(code))
	var online *redis.IntCmd
	if opts.Presence {
		cutoff := time.Now().Add(-presenceStale).Unix()
		online = pipe.ZCount(ctx, presenceKey(code), fmt.Sprint(cutoff), "+inf")
	}
	var ids *redis.StringSliceCmd
	if opts.ContentIDs {
		ids = pipe.LRange(ctx, contentListKey(code), 0, -1)
	}
	_, _ = pipe.Exec(ctx) // per-command results are checked below; redis.Nil is expected

	// Real failures (connection errors) must surface; a missing key is normal.
	for _, c := range []redis.Cmder{bf, bfTTL, sess, pttl} {
		if err := c.Err(); err != nil && err != redis.Nil {
			return nil, err
		}
	}
	out := &Lookup{}
	if n, err := bf.Int(); err == nil {
		out.Failures = n
	}
	out.LockTTL = bfTTL.Val()

	if raw, err := sess.Bytes(); err == nil {
		var rec sessionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		m := rec.toModel()
		out.Session = &m
		out.TTL = pttl.Val()
	}
	if online != nil {
		n, err := online.Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		out.Online = int(n)
	}
	if ids != nil {
		v, err := ids.Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		out.ContentIDs = v
	}
	return out, nil
}
