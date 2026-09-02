package handlers

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/ws"
)

type WebSocketHandler struct {
	store    *redisclient.Store
	upgrader websocket.Upgrader
}

func NewWebSocketHandler(store *redisclient.Store, allowedOrigins []string) *WebSocketHandler {
	return &WebSocketHandler{
		store: store,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return slices.Contains(allowedOrigins, r.Header.Get("Origin"))
			},
		},
	}
}

// HandleConnection — GET /ws/sessions/:code
func (h *WebSocketHandler) HandleConnection(c *gin.Context) {
	code := c.Param("code")
	if _, err := h.store.GetSession(c.Request.Context(), code); err != nil {
		status := http.StatusInternalServerError
		if err == redis.Nil {
			status = http.StatusGone
		}
		c.JSON(status, gin.H{"error": "session not found or expired"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	hub := ws.GetOrCreateHub(h.store, code)
	hub.Register(conn)
	defer hub.Unregister(conn)

	// Read loop only exists to detect client disconnects (ping/pong is handled
	// by the gorilla/websocket library's default handlers).
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}
