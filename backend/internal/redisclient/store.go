package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/models"
)

// Store wraps a Redis client with helpers for the ephemeral session lifecycle.
// Every key related to a session is bound to the SAME absolute TTL as the
// session itself — nothing is ever allowed to outlive the original 5 minutes.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(addr, password string, ttl time.Duration) *Store {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password})
	return &Store{rdb: rdb, ttl: ttl}
}

func sessionKey(code string) string         { return fmt.Sprintf("session:%s", code) }
func contentListKey(code string) string     { return fmt.Sprintf("session:%s:content", code) }
func contentItemKey(code, id string) string { return fmt.Sprintf("session:%s:content:%s", code, id) }
func participantsKey(code string) string    { return fmt.Sprintf("session:%s:participants", code) }
func wsChannel(code string) string          { return fmt.Sprintf("ws:%s", code) }

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
	pipe.Expire(ctx, participantsKey(sess.Code), s.ttl) // no-op if not created yet, harmless
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
	keys := []string{sessionKey(code), contentListKey(code), participantsKey(code)}
	for _, id := range ids {
		keys = append(keys, contentItemKey(code, id))
	}
	return s.rdb.Del(ctx, keys...).Err()
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
