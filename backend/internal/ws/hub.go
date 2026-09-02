package ws

import (
	"context"
	"log"
	"sync"

	"github.com/gorilla/websocket"

	"ephemeral-share/internal/redisclient"
)

// Hub fans out Redis pub/sub events for one session code to all locally
// connected WebSocket clients. One Hub per active session per backend instance;
// Redis pub/sub is what makes this work correctly across multiple instances.
type Hub struct {
	code    string
	store   *redisclient.Store
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
	cancel  context.CancelFunc
}

var (
	hubsMu sync.Mutex
	hubs   = map[string]*Hub{}
)

// GetOrCreateHub returns the hub for a session code, starting its Redis
// subscription loop the first time it's needed on this instance.
func GetOrCreateHub(store *redisclient.Store, code string) *Hub {
	hubsMu.Lock()
	defer hubsMu.Unlock()
	if h, ok := hubs[code]; ok {
		return h
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{code: code, store: store, clients: map[*websocket.Conn]bool{}, cancel: cancel}
	hubs[code] = h
	go h.listen(ctx)
	return h
}

func (h *Hub) listen(ctx context.Context) {
	sub := h.store.Subscribe(ctx, h.code)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			h.broadcast([]byte(msg.Payload))
		}
	}
}

func (h *Hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			conn.Close()
			delete(h.clients, conn)
		}
	}
}

// Register adds a new client connection to this hub.
func (h *Hub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = true
}

// Unregister removes a client and tears down the hub if it was the last one.
func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	empty := len(h.clients) == 0
	h.mu.Unlock()
	conn.Close()

	if empty {
		hubsMu.Lock()
		defer hubsMu.Unlock()
		if len(h.clients) == 0 { // re-check under hubsMu to avoid a race with Register
			h.cancel()
			delete(hubs, h.code)
			log.Printf("hub for session %s torn down", h.code)
		}
	}
}

// NotifyExpired force-broadcasts a session_expired event and clears the hub.
func NotifyExpired(code string) {
	hubsMu.Lock()
	h, ok := hubs[code]
	hubsMu.Unlock()
	if !ok {
		return
	}
	h.broadcast([]byte(`{"event":"session_expired"}`))
	h.mu.Lock()
	for conn := range h.clients {
		conn.Close()
	}
	h.mu.Unlock()
	h.cancel()
	hubsMu.Lock()
	delete(hubs, code)
	hubsMu.Unlock()
}
