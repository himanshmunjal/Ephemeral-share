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

// AddContent appends a content item and broadcasts it, in ONE round trip.
// `remaining` is the session's remaining TTL (from Lookup), so children never
// outlive the session.
func (s *Store) AddContent(ctx context.Context, item models.ContentItem, remaining time.Duration) error {
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
	// Publish for WebSocket fan-out across instances.
	pipe.Publish(ctx, wsChannel(item.SessionCode), mustJSON(map[string]any{
		"event": "content_added",
		"item":  item,
	}))
	_, err = pipe.Exec(ctx)
	return err
}

// AddPendingUpload records a file item whose bytes haven't landed in object
// storage yet. It stays out of the content list (and isn't broadcast) until
// TakePendingUpload promotes it via AddContent.
func (s *Store) AddPendingUpload(ctx context.Context, item models.ContentItem, remaining time.Duration) error {
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

// GetContentItems loads the given content ids with a single MGET (it used to
// be one round trip per item, which dominated page-load time on a remote Redis).
func (s *Store) GetContentItems(ctx context.Context, code string, ids []string) ([]models.ContentItem, error) {
	items := make([]models.ContentItem, 0, len(ids))
	if len(ids) == 0 {
		return items, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = contentItemKey(code, id)
	}
	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for _, v := range vals {
		str, ok := v.(string)
		if !ok {
			continue // that item's key expired at the same instant; skip
		}
		var rec contentRecord
		if err := json.Unmarshal([]byte(str), &rec); err == nil {
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
func (s *Store) ReserveQuota(ctx context.Context, code, field string, maxCount int, size, maxBytes int64, remaining time.Duration) (int, error) {
	if remaining <= 0 {
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
	pipe.Publish(ctx, wsChannel(code), mustJSON(map[string]any{"event": "content_removed", "id": id}))
	_, err := pipe.Exec(ctx)
	return err
}

// ---- presence (live user count) ------------------------------------------

const presenceStale = 75 * time.Second

// presenceSnapshotLua builds the {id, who, id, who, ...} list of live
// connections. Shared by the join and snapshot scripts so a join returns the
// roster in the same round trip. KEYS[1]=presence zset, KEYS[2]=who hash,
// ARGV[2]=stale-before timestamp.
const presenceSnapshotLua = `
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], ARGV[2], '+inf')
local out = {}
for _, id in ipairs(ids) do
  local w = redis.call('HGET', KEYS[2], id)
  if not w then w = '' end
  out[#out + 1] = id
  out[#out + 1] = w
end
`

// presenceJoinScript drops stale members, admits the connection only if the
// room has space, records its encrypted identity, and returns the roster.
// ARGV: now, staleBefore, member, max, ttlSeconds, who. Returns nil when full.
var presenceJoinScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[2])
local exists = redis.call('ZSCORE', KEYS[1], ARGV[3])
if (not exists) and redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[4]) then return false end
redis.call('ZADD', KEYS[1], ARGV[1], ARGV[3])
redis.call('EXPIRE', KEYS[1], ARGV[5])
if ARGV[6] ~= '' then
  redis.call('HSET', KEYS[2], ARGV[3], ARGV[6])
  redis.call('EXPIRE', KEYS[2], ARGV[5])
end
` + presenceSnapshotLua + `
return out
`)

var presenceSnapshotScript = redis.NewScript(presenceSnapshotLua + `
return out
`)

// PresenceUser is one live connection and its encrypted display identity.
type PresenceUser struct {
	ID  string `json:"id"`
	Who string `json:"who"`
}

func parseRoster(v any) []PresenceUser {
	flat, _ := v.([]any)
	users := make([]PresenceUser, 0, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		id, _ := flat[i].(string)
		who, _ := flat[i+1].(string)
		users = append(users, PresenceUser{ID: id, Who: who})
	}
	return users
}

func snapshotOf(users []PresenceUser) map[string]any {
	return map[string]any{"event": "presence", "count": len(users), "users": users}
}

// PresenceJoin registers a connection in ONE round trip and returns the roster
// snapshot to send. ok=false means the room is full. `who` is the client's
// encrypted display identity (opaque to the server).
func (s *Store) PresenceJoin(ctx context.Context, code, connID, who string, max int, remaining time.Duration) (snapshot map[string]any, ok bool, err error) {
	if remaining <= 0 {
		return nil, false, fmt.Errorf("session expired")
	}
	now := time.Now().Unix()
	res, err := presenceJoinScript.Run(ctx, s.rdb, []string{presenceKey(code), whoKey(code)},
		now, now-int64(presenceStale.Seconds()), connID, max, int64(remaining.Seconds())+1, who).Result()
	if err == redis.Nil || res == nil {
		return nil, false, nil // room is full
	}
	if err != nil {
		return nil, false, err
	}
	return snapshotOf(parseRoster(res)), true, nil
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

// PresenceSnapshot returns the current roster in one round trip.
func (s *Store) PresenceSnapshot(ctx context.Context, code string) map[string]any {
	now := time.Now().Unix()
	res, err := presenceSnapshotScript.Run(ctx, s.rdb, []string{presenceKey(code), whoKey(code)}, now, now-int64(presenceStale.Seconds())).Result()
	if err != nil {
		return snapshotOf(nil)
	}
	return snapshotOf(parseRoster(res))
}

// PublishSnapshot broadcasts an already-built roster to every client/instance.
func (s *Store) PublishSnapshot(ctx context.Context, code string, snapshot map[string]any) {
	s.rdb.Publish(ctx, wsChannel(code), mustJSON(snapshot))
}

// PublishPresence builds and broadcasts the current roster.
func (s *Store) PublishPresence(ctx context.Context, code string) {
	s.PublishSnapshot(ctx, code, s.PresenceSnapshot(ctx, code))
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
