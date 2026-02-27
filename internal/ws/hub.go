package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ──────────────────────────────────────────────────────────────────────────────
// Tunables
// ──────────────────────────────────────────────────────────────────────────────

const (
	writeDeadline  = 10 * time.Second
	pingInterval   = 30 * time.Second
	pongWait       = 35 * time.Second // must be > pingInterval
	maxMessageSize = 512              // bytes; clients only send pongs
	sendBufferSize = 256              // messages in each client send channel
)

// ──────────────────────────────────────────────────────────────────────────────
// Client
// ──────────────────────────────────────────────────────────────────────────────

// Client represents one connected WebSocket endpoint.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte // buffered outbound message queue
	userID uuid.UUID   // zero-value = anonymous
}

// ──────────────────────────────────────────────────────────────────────────────
// Hub
// ──────────────────────────────────────────────────────────────────────────────

// Hub maintains the set of active clients and routes broadcast messages.
// Run() must be called in a dedicated goroutine before ServeWs is used.
type Hub struct {
	// Registered clients and their concurrency guard.
	mu      sync.RWMutex
	clients map[*Client]bool

	// channels consumed by Run()
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client

	// JWT signing key (optional – if empty, all connections are anonymous)
	jwtSecret []byte

	// upgrader is safe for concurrent use after construction.
	upgrader websocket.Upgrader
}

// NewHub creates a Hub ready to be started with Run().
// jwtSecret may be nil; WS connections will then be treated as anonymous.
func NewHub(jwtSecret []byte, allowedOrigins []string) *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 512),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		jwtSecret:  jwtSecret,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				if len(allowedOrigins) == 0 {
					return true // dev mode: allow all
				}
				origin := r.Header.Get("Origin")
				for _, o := range allowedOrigins {
					if o == "*" || o == origin {
						return true
					}
				}
				return false
			},
		},
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Run — hub event loop
// ──────────────────────────────────────────────────────────────────────────────

// Run processes registration, unregistration, and broadcast events
// sequentially.  Call it once as a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Client's buffer full — drop the message for this client.
					// The writePump will detect a stalled connection separately.
				}
			}
			h.mu.RUnlock()
		}
	}
}

// ConnectedCount returns the current number of connected clients.
func (h *Hub) ConnectedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ──────────────────────────────────────────────────────────────────────────────
// ServeWs — HTTP → WebSocket upgrade
// ──────────────────────────────────────────────────────────────────────────────

// ServeWs upgrades an HTTP request to a WebSocket connection, optionally
// authenticates the caller via a JWT in the ?token= query parameter, and
// starts the read/write pumps.
func (h *Hub) ServeWs(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws.ServeWs: upgrade failed: %v", err)
		return
	}

	var userID uuid.UUID // zero = anonymous
	if token := r.URL.Query().Get("token"); token != "" && len(h.jwtSecret) > 0 {
		userID = h.parseJWT(token)
	}

	client := &Client{
		hub:    h,
		conn:   conn,
		send:   make(chan []byte, sendBufferSize),
		userID: userID,
	}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

// parseJWT extracts the user UUID from a signed token.
// Returns uuid.Nil on any failure (treated as anonymous).
func (h *Hub) parseJWT(tokenString string) uuid.UUID {
	tok, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.jwtSecret, nil
	})
	if err != nil || !tok.Valid {
		return uuid.Nil
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil
	}
	sub, _ := claims.GetSubject()
	id, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// ──────────────────────────────────────────────────────────────────────────────
// Client pumps
// ──────────────────────────────────────────────────────────────────────────────

// writePump drains the client's send channel and writes messages to the
// WebSocket connection.  It also sends ping frames every pingInterval.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if !ok {
				// Hub closed the channel.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump reads frames from the WebSocket connection.  Only pong messages
// are handled (they reset the read deadline).  All other inbound messages are
// discarded — this is a server-push-only protocol.  When the connection drops
// the client is unregistered.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws.readPump: unexpected close for user %s: %v", c.userID, err)
			}
			return
		}
		// All inbound messages are silently dropped; server is push-only.
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Broadcast helpers — implement scheduler.WsHub and service.Broadcaster
// ──────────────────────────────────────────────────────────────────────────────

// BroadcastPriceUpdate serialises and broadcasts a PriceUpdateMessage.
func (h *Hub) BroadcastPriceUpdate(msg PriceUpdateMessage) {
	h.broadcastJSON(msg)
}

// BroadcastMarketResolved serialises and broadcasts a MarketResolvedMessage.
func (h *Hub) BroadcastMarketResolved(msg MarketResolvedMessage) {
	h.broadcastJSON(msg)
}

// BroadcastNewMarket serialises and broadcasts a NewMarketMessage.
func (h *Hub) BroadcastNewMarket(msg NewMarketMessage) {
	h.broadcastJSON(msg)
}

// BroadcastBetPlaced serialises and broadcasts a BetPlacedMessage.
func (h *Hub) BroadcastBetPlaced(msg BetPlacedMessage) {
	h.broadcastJSON(msg)
}

// BroadcastMarketUpdate satisfies the service.Broadcaster interface.
// It converts a domain.MarketSummary into a PriceUpdateMessage.
func (h *Hub) BroadcastMarketUpdate(summary *domain.MarketSummary) {
	data, err := json.Marshal(summary)
	if err != nil {
		return
	}
	select {
	case h.broadcast <- data:
	default:
	}
}

// broadcastJSON is the common marshalling path.
func (h *Hub) broadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws.Hub: marshal error: %v", err)
		return
	}
	select {
	case h.broadcast <- data:
	default:
		log.Printf("ws.Hub: broadcast channel full, message dropped")
	}
}

// SendError writes an error message directly to one client's send channel.
func (h *Hub) SendError(client *Client, code, message string) {
	data, err := json.Marshal(ErrorMessage{
		Type:    MsgTypeError,
		Code:    code,
		Message: message,
	})
	if err != nil {
		return
	}
	select {
	case client.send <- data:
	default:
	}
}
