package redisclient

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

// roundTripCounter counts network round trips: one per command, and one per
// whole pipeline/transaction. Round trips (not command counts) are what make a
// request slow against a remote Redis, so tests use this to pin the budget.
type roundTripCounter struct{ n *atomic.Int64 }

func (roundTripCounter) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}

func (c roundTripCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		c.n.Add(1)
		return next(ctx, cmd)
	}
}

func (c roundTripCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		c.n.Add(1)
		return next(ctx, cmds)
	}
}

// CountRoundTrips starts counting this store's Redis round trips into n.
func (s *Store) CountRoundTrips(n *atomic.Int64) { s.rdb.AddHook(roundTripCounter{n: n}) }
