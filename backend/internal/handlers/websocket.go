package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/config"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/ws"
)

const (
	pingEvery  = 25 * time.Second
	readWindow = 60 * time.Second // must exceed pingEvery
)

type WebSocketHandler struct {
	store    *redisclient.Store
	guard    *Guard
	upgrader websocket.Upgrader
}

func NewWebSocketHandler(store *redisclient.Store, cfg *config.Config, guard *Guard) *WebSocketHandler {
	return &WebSocketHandler{
		store: store,
		guard: guard,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return slices.Contains(cfg.AllowedOrigins, r.Header.Get("Origin"))
			},
		},
	}
}

// HandleConnection — GET /ws/sessions/:code
func (h *WebSocketHandler) HandleConnection(c *gin.Context) {
	if h.guard.Locked(c) {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")
	sess, err := h.store.GetSession(ctx, code)
	if err != nil {
		status := http.StatusInternalServerError
		if err == redis.Nil {
			status = http.StatusGone
			h.guard.Miss(c)
		}
		c.JSON(status, gin.H{"error": "session not found or expired"})
		return
	}

	// Atomic participant cap: the connection is admitted only if the room has
	// space (stale heartbeats are swept first), so reloads don't burn slots.
	connID := uuid.NewString()
	_, ok, err := h.store.PresenceJoin(ctx, code, connID, validWho(c.Query("who")), sess.MaxParticipants)
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session not found or expired"})
		return
	}
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "this session is full", "code": "full"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.store.PresenceLeave(context.Background(), code, connID)
		return
	}
	conn.SetReadLimit(4096) // clients never need to send us anything big
	_ = conn.SetReadDeadline(time.Now().Add(readWindow))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readWindow))
		h.store.PresenceTouch(context.Background(), code, connID)
		return nil
	})

	hub := ws.GetOrCreateHub(h.store, code)
	hub.Register(conn)

	// Tell the newcomer the current count directly (its pub/sub subscription
	// may not be live yet), and everyone else via Redis.
	first, _ := json.Marshal(h.store.PresenceSnapshot(context.Background(), code))
	_ = hub.WriteTo(conn, first)
	h.store.PublishPresence(context.Background(), code)

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := hub.Ping(conn); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	defer func() {
		close(done)
		hub.Unregister(conn)
		h.store.PresenceLeave(context.Background(), code, connID)
		h.store.PublishPresence(context.Background(), code)
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}
