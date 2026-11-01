package redisclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/models"
)

// Ping reports whether Redis is reachable (used by /health).
func (s *Store) Ping(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }

// Store wraps a Redis client with helpers for the ephemeral session lifecycle.
// Every key related to a session is bound to the SAME absolute TTL as the
// session itself — nothing is ever allowed to outlive the original TTL.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// Options builds client options; useTLS is needed for managed Redis that only
// accepts encrypted connections (Upstash, many cloud offerings).
func Options(addr, password string, useTLS bool) *redis.Options {
	o := &redis.Options{Addr: addr, Password: password}
	if useTLS {
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return o
}

func New(addr, password string, ttl time.Duration, useTLS bool) *Store {
	rdb := redis.NewClient(Options(addr, password, useTLS))
	return &Store{rdb: rdb, ttl: ttl}
}

func sessionKey(code string) string         { return fmt.Sprintf("session:%s", code) }
func contentListKey(code string) string     { return fmt.Sprintf("session:%s:content", code) }
func contentItemKey(code, id string) string { return fmt.Sprintf("session:%s:content:%s", code, id) }
func participantsKey(code string) string    { return fmt.Sprintf("session:%s:participants", code) }
func pendingUploadKey(code, id string) string {
	return fmt.Sprintf("session:%s:pending:%s", code, id)
}
func usageKey(code string) string    { return fmt.Sprintf("session:%s:usage", code) }
func presenceKey(code string) string { return fmt.Sprintf("session:%s:presence", code) }
func whoKey(code string) string      { return fmt.Sprintf("session:%s:who", code) }
func lockKey(ip string) string       { return fmt.Sprintf("bf:%s", ip) }
func wsChannel(code string) string   { return fmt.Sprintf("ws:%s", code) }

// sessionRecord/contentRecord are the on-disk shapes used for Redis persistence.
// models.Session and models.ContentItem carry `json:"-"` on OwnerToken and
// StorageKey respectively so those fields never leak into API responses — but
// that means marshaling the API structs directly would also silently drop
// them from Redis storage. These records restore those fields for storage.
type sessionRecord struct {
	Code            string             `json:"code"`
	Type            models.SessionType `json:"type"`
	MaxParticipants int                `json:"max_participants"`
	CreatedAt       time.Time          `json:"created_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	OwnerToken      string             `json:"owner_token"`
}

func toSessionRecord(sess models.Session) sessionRecord {
	return sessionRecord{
		Code:            sess.Code,
		Type:            sess.Type,
		MaxParticipants: sess.MaxParticipants,
		CreatedAt:       sess.CreatedAt,
		ExpiresAt:       sess.ExpiresAt,
		OwnerToken:      sess.OwnerToken,
	}
}

func (r sessionRecord) toModel() models.Session {
	return models.Session{
		Code:            r.Code,
		Type:            r.Type,
		MaxParticipants: r.MaxParticipants,
		CreatedAt:       r.CreatedAt,
		ExpiresAt:       r.ExpiresAt,
		OwnerToken:      r.OwnerToken,
	}
}

type contentRecord struct {
	ID          string             `json:"id"`
	SessionCode string             `json:"session_code"`
	Kind        models.ContentKind `json:"kind"`
	ContentType string             `json:"content_type"`
	SizeBytes   int64              `json:"size_bytes,omitempty"`
	StorageKey  string             `json:"storage_key,omitempty"`
	TextPayload string             `json:"text_payload,omitempty"`
	Filename    string             `json:"filename,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UploaderID  string             `json:"uploader_id,omitempty"`
	Who         string             `json:"who,omitempty"`
}

func toContentRecord(item models.ContentItem) contentRecord {
	return contentRecord{
		ID:          item.ID,
		SessionCode: item.SessionCode,
		Kind:        item.Kind,
		ContentType: item.ContentType,
		SizeBytes:   item.SizeBytes,
		StorageKey:  item.StorageKey,
		TextPayload: item.TextPayload,
		Filename:    item.Filename,
		CreatedAt:   item.CreatedAt,
		UploaderID:  item.UploaderID,
		Who:         item.Who,
	}
}

func (r contentRecord) toModel() models.ContentItem {
	return models.ContentItem{
		ID:          r.ID,
		SessionCode: r.SessionCode,
		Kind:        r.Kind,
		ContentType: r.ContentType,
		SizeBytes:   r.SizeBytes,
		StorageKey:  r.StorageKey,
		TextPayload: r.TextPayload,
		Filename:    r.Filename,
		CreatedAt:   r.CreatedAt,
		UploaderID:  r.UploaderID,
		Who:         r.Who,
	}
}

// CreateSession writes the session hash and sets the absolute TTL.
func (s *Store) CreateSession(ctx context.Context, sess models.Session) error {
	data, err := json.Marshal(toSessionRecord(sess))
	if err != nil {
		return err
	}
	key := sessionKey(sess.Code)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, key, data, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

// GetSession fetches a session by code. Returns redis.Nil if not found/expired.
func (s *Store) GetSession(ctx context.Context, code string) (*models.Session, error) {
	data, err := s.rdb.Get(ctx, sessionKey(code)).Bytes()
	if err != nil {
		return nil, err
	}
	var rec sessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	sess := rec.toModel()
	return &sess, nil
}

// TTLRemaining returns how much time is left on the session, for client countdowns.
func (s *Store) TTLRemaining(ctx context.Context, code string) (time.Duration, error) {
	return s.rdb.TTL(ctx, sessionKey(code)).Result()
}

// CodeExists checks for collisions when generating a new code.
func (s *Store) CodeExists(ctx context.Context, code string) (bool, error) {
	n, err := s.rdb.Exists(ctx, sessionKey(code)).Result()
	return n > 0, err
}

// AddContent appends a content item, capping its TTL to the session's remaining TTL.
func (s *Store) AddContent(ctx context.Context, item models.ContentItem) error {
	remaining, err := s.TTLRemaining(ctx, item.SessionCode)
	if err != nil {
		return err
	}
	if remaining <= 0 {
		return fmt.Errorf("session expired")
	}
	data, err := json.Marshal(toContentRecord(item))
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.RPush(ctx, contentListKey(item.SessionCode), item.ID)
	pipe.Expire(ctx, contentListKey(item.SessionCode), remaining)
	pipe.Set(ctx, contentItemKey(item.SessionCode, item.ID), data, remaining)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	// Publish for WebSocket fanout across instances.
	s.rdb.Publish(ctx, wsChannel(item.SessionCode), mustJSON(map[string]any{
		"event": "content_added",
		"item":  item,
	}))
	return nil
}

// AddPendingUpload records a file item whose bytes haven't landed in object
// storage yet. It stays out of the content list (and isn't broadcast) until
// TakePendingUpload promotes it via AddContent.
func (s *Store) AddPendingUpload(ctx context.Context, item models.ContentItem) error {
	remaining, err := s.TTLRemaining(ctx, item.SessionCode)
	if err != nil {
		return err
	}
	if remaining <= 0 {
		return fmt.Errorf("session expired")
	}
	data, err := json.Marshal(toContentRecord(item))
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, pendingUploadKey(item.SessionCode, item.ID), data, remaining).Err()
}

// GetPendingUpload returns a pending upload without consuming it. Returns
// redis.Nil if there is no such pending upload.
func (s *Store) GetPendingUpload(ctx context.Context, code, id string) (*models.ContentItem, error) {
	return decodeContent(s.rdb.Get(ctx, pendingUploadKey(code, id)).Bytes())
}

// TakePendingUpload atomically removes and returns a pending upload, so each
// upload can be completed (and broadcast) only once. Returns redis.Nil if it
// was already taken.
func (s *Store) TakePendingUpload(ctx context.Context, code, id string) (*models.ContentItem, error) {
	return decodeContent(s.rdb.GetDel(ctx, pendingUploadKey(code, id)).Bytes())
}

func decodeContent(data []byte, err error) (*models.ContentItem, error) {
	if err != nil {
		return nil, err
	}
	var rec contentRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	item := rec.toModel()
	return &item, nil
}

// ListContent returns all content items currently in the session, in order.
func (s *Store) ListContent(ctx context.Context, code string) ([]models.ContentItem, error) {
	ids, err := s.rdb.LRange(ctx, contentListKey(code), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	items := make([]models.ContentItem, 0, len(ids))
	for _, id := range ids {
		data, err := s.rdb.Get(ctx, contentItemKey(code, id)).Bytes()
		if err == redis.Nil {
			continue // item's individual key expired at exactly the same instant; skip
		}
		if err != nil {
			return nil, err
		}
		var rec contentRecord
		if err := json.Unmarshal(data, &rec); err == nil {
			items = append(items, rec.toModel())
		}
	}
	return items, nil
}

// JoinParticipant enforces max_participants and registers a participant.
func (s *Store) JoinParticipant(ctx context.Context, code, participantID string, max int) (bool, error) {
	remaining, err := s.TTLRemaining(ctx, code)
	if err != nil || remaining <= 0 {
		return false, fmt.Errorf("session expired")
	}
	count, err := s.rdb.SCard(ctx, participantsKey(code)).Result()
	if err != nil {
		return false, err
	}
	if max > 0 && int(count) >= max {
		return false, nil // at capacity
	}
	pipe := s.rdb.TxPipeline()
	pipe.SAdd(ctx, participantsKey(code), participantID)
	pipe.Expire(ctx, participantsKey(code), remaining)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	s.rdb.Publish(ctx, wsChannel(code), mustJSON(map[string]any{
		"event":          "participant_joined",
		"participant_id": participantID,
	}))
	return true, nil
}

// Subscribe returns a pub/sub subscription for a session's WebSocket channel.
func (s *Store) Subscribe(ctx context.Context, code string) *redis.PubSub {
	return s.rdb.Subscribe(ctx, wsChannel(code))
}

// DeleteSession purges all keys for a session immediately (owner termination or cleanup).
func (s *Store) DeleteSession(ctx context.Context, code string) error {
	ids, _ := s.rdb.LRange(ctx, contentListKey(code), 0, -1).Result()
	keys := []string{sessionKey(code), contentListKey(code), participantsKey(code), usageKey(code), presenceKey(code), whoKey(code)}
	for _, id := range ids {
		keys = append(keys, contentItemKey(code, id))
	}
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		return err
	}
	// An explicit DEL fires no "expired" keyspace event, so tell connected
	// clients (on every instance) directly that the session is gone.
	s.rdb.Publish(ctx, wsChannel(code), mustJSON(map[string]any{"event": "session_ended"}))
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- quotas -------------------------------------------------------------

// Reservation result codes from reserveScript.
const (
	ReserveOK           = 1
	ReserveTooManyFiles = -1
	ReserveTooBig       = -2
)

// reserveScript atomically checks and bumps the session's usage counters so
// two concurrent uploads can't both slip under the limit. KEYS[1]=usage hash,
// ARGV: field ("files"|"texts"), maxCount, size, maxBytes, ttlSeconds.
var reserveScript = redis.NewScript(`
local count = tonumber(redis.call('HGET', KEYS[1], ARGV[1]) or '0')
local bytes = tonumber(redis.call('HGET', KEYS[1], 'bytes') or '0')
local size = tonumber(ARGV[3])
if count + 1 > tonumber(ARGV[2]) then return -1 end
if bytes + size > tonumber(ARGV[4]) then return -2 end
redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
if size > 0 then redis.call('HINCRBY', KEYS[1], 'bytes', size) end
redis.call('EXPIRE', KEYS[1], ARGV[5])
return 1
`)

// ReserveQuota claims one slot (and `size` bytes) against the session quotas.
// field is "files" or "texts".
func (s *Store) ReserveQuota(ctx context.Context, code, field string, maxCount int, size, maxBytes int64) (int, error) {
	remaining, err := s.TTLRemaining(ctx, code)
	if err != nil || remaining <= 0 {
		return 0, fmt.Errorf("session expired")
	}
	secs := int64(remaining.Seconds()) + 1
	n, err := reserveScript.Run(ctx, s.rdb, []string{usageKey(code)}, field, maxCount, size, maxBytes, secs).Int()
	return n, err
}

// ReleaseQuota gives back a slot and its bytes (failed/cancelled/deleted item).
func (s *Store) ReleaseQuota(ctx context.Context, code, field string, size int64) {
	pipe := s.rdb.TxPipeline()
	pipe.HIncrBy(ctx, usageKey(code), field, -1)
	if size > 0 {
		pipe.HIncrBy(ctx, usageKey(code), "bytes", -size)
	}
	_, _ = pipe.Exec(ctx)
}

// ---- content removal ----------------------------------------------------

// GetContentItem returns one live (completed) content item.
func (s *Store) GetContentItem(ctx context.Context, code, id string) (*models.ContentItem, error) {
	return decodeContent(s.rdb.Get(ctx, contentItemKey(code, id)).Bytes())
}

// RemoveContent deletes a completed item and tells every client.
func (s *Store) RemoveContent(ctx context.Context, code, id string) error {
	pipe := s.rdb.TxPipeline()
	pipe.LRem(ctx, contentListKey(code), 0, id)
	pipe.Del(ctx, contentItemKey(code, id))
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	s.rdb.Publish(ctx, wsChannel(code), mustJSON(map[string]any{"event": "content_removed", "id": id}))
	return nil
}

// ---- presence (live user count) ------------------------------------------

const presenceStale = 75 * time.Second

// presenceJoinScript drops stale members, then admits the connection only if
// the cap isn't reached. Returns the new count, or -1 when the room is full.
// KEYS[1]=presence zset; ARGV: now, staleBefore, member, max, ttlSeconds.
var presenceJoinScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[2])
local exists = redis.call('ZSCORE', KEYS[1], ARGV[3])
if (not exists) and redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[4]) then return -1 end
redis.call('ZADD', KEYS[1], ARGV[1], ARGV[3])
redis.call('EXPIRE', KEYS[1], ARGV[5])
return redis.call('ZCARD', KEYS[1])
`)

// PresenceCount returns the number of live connections in the session.
func (s *Store) PresenceCount(ctx context.Context, code string) (int, error) {
	cutoff := time.Now().Add(-presenceStale).Unix()
	n, err := s.rdb.ZCount(ctx, presenceKey(code), fmt.Sprint(cutoff), "+inf").Result()
	return int(n), err
}

// PresenceJoin registers a connection. ok=false means the room is full. `who`
// is the client's encrypted display identity (opaque to the server).
func (s *Store) PresenceJoin(ctx context.Context, code, connID, who string, max int) (count int, ok bool, err error) {
	remaining, err := s.TTLRemaining(ctx, code)
	if err != nil || remaining <= 0 {
		return 0, false, fmt.Errorf("session expired")
	}
	now := time.Now().Unix()
	n, err := presenceJoinScript.Run(ctx, s.rdb, []string{presenceKey(code)},
		now, now-int64(presenceStale.Seconds()), connID, max, int64(remaining.Seconds())+1).Int()
	if err != nil {
		return 0, false, err
	}
	if n < 0 {
		return 0, false, nil
	}
	if who != "" {
		pipe := s.rdb.TxPipeline()
		pipe.HSet(ctx, whoKey(code), connID, who)
		pipe.Expire(ctx, whoKey(code), remaining)
		_, _ = pipe.Exec(ctx)
	}
	return n, true, nil
}

// PresenceTouch refreshes a connection's heartbeat.
func (s *Store) PresenceTouch(ctx context.Context, code, connID string) {
	s.rdb.ZAddXX(ctx, presenceKey(code), redis.Z{Score: float64(time.Now().Unix()), Member: connID})
}

// PresenceLeave removes a connection.
func (s *Store) PresenceLeave(ctx context.Context, code, connID string) {
	pipe := s.rdb.TxPipeline()
	pipe.ZRem(ctx, presenceKey(code), connID)
	pipe.HDel(ctx, whoKey(code), connID)
	_, _ = pipe.Exec(ctx)
}

// PresenceUser is one live connection and its encrypted display identity.
type PresenceUser struct {
	ID  string `json:"id"`
	Who string `json:"who"`
}

// PresenceUsers lists live connections with their encrypted identities.
func (s *Store) PresenceUsers(ctx context.Context, code string) ([]PresenceUser, error) {
	cutoff := time.Now().Add(-presenceStale).Unix()
	ids, err := s.rdb.ZRangeByScore(ctx, presenceKey(code), &redis.ZRangeBy{Min: fmt.Sprint(cutoff), Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	users := make([]PresenceUser, 0, len(ids))
	if len(ids) == 0 {
		return users, nil
	}
	whos, err := s.rdb.HMGet(ctx, whoKey(code), ids...).Result()
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		w, _ := whos[i].(string)
		users = append(users, PresenceUser{ID: id, Who: w})
	}
	return users, nil
}

// PresenceSnapshot is the payload sent to clients: count plus who is here.
func (s *Store) PresenceSnapshot(ctx context.Context, code string) map[string]any {
	users, err := s.PresenceUsers(ctx, code)
	if err != nil {
		users = []PresenceUser{}
	}
	return map[string]any{"event": "presence", "count": len(users), "users": users}
}

// PublishPresence broadcasts the current snapshot to every client/instance.
func (s *Store) PublishPresence(ctx context.Context, code string) {
	s.rdb.Publish(ctx, wsChannel(code), mustJSON(s.PresenceSnapshot(ctx, code)))
}

// ---- brute-force lockout ---------------------------------------------------

// IsLocked reports whether this IP has used up its failed-lookup budget.
func (s *Store) IsLocked(ctx context.Context, ip string, maxFailures int) (bool, time.Duration) {
	n, err := s.rdb.Get(ctx, lockKey(ip)).Int()
	if err != nil || n < maxFailures {
		return false, 0
	}
	ttl, _ := s.rdb.TTL(ctx, lockKey(ip)).Result()
	return true, ttl
}

// RecordFailure counts one wrong-code lookup; the window starts at the first
// failure and the counter (and lock) clears when it elapses.
func (s *Store) RecordFailure(ctx context.Context, ip string, window time.Duration) {
	pipe := s.rdb.TxPipeline()
	pipe.Incr(ctx, lockKey(ip))
	pipe.ExpireNX(ctx, lockKey(ip), window)
	_, _ = pipe.Exec(ctx)
}

// SessionExists reports whether a code maps to a live session (orphan sweep).
func (s *Store) SessionExists(ctx context.Context, code string) (bool, error) {
	return s.CodeExists(ctx, code)
}
