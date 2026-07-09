// Package proto defines the handshake types and shared constants used by
// the liveurl agent and the liveurld server.
package proto

import (
	"bufio"
	"encoding/json"
	"net"
	"time"
)

const (
	Version = 1

	DefaultTunnelAddr  = "127.0.0.1:4443"
	DefaultPublicAddr  = "127.0.0.1:8080"
	DefaultControlAddr = "127.0.0.1:8081"
	DefaultControlURL  = "http://127.0.0.1:8081"

	// DefaultPublicHost is the host (with port) under which tunnels are
	// exposed during local development. lvh.me and all of its subdomains
	// resolve to 127.0.0.1.
	DefaultPublicHost = "lvh.me:8080"

	// HeaderBuffered marks a request that was buffered while the agent was
	// offline and is being replayed after reconnect.
	HeaderBuffered = "X-Liveurl-Buffered"
	// HeaderOriginalTimestamp carries the original receipt time (RFC 3339)
	// of a buffered request.
	HeaderOriginalTimestamp = "X-Liveurl-Original-Timestamp"
	// HeaderEventID is returned on buffered responses so callers can
	// correlate queue entries.
	HeaderEventID = "X-Liveurl-Event-Id"

	// MaxWebhookBody caps request bodies persisted to the webhook queue.
	MaxWebhookBody = 5 << 20
)

// Handshake is sent by the agent as the only message on the first yamux
// stream after connecting.
type Handshake struct {
	Version     int      `json:"version"`
	Token       string   `json:"token"`
	Subdomain   string   `json:"subdomain,omitempty"`
	BufferRules []string `json:"buffer_rules,omitempty"`
}

// HandshakeReply is the server's answer on the same stream.
type HandshakeReply struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Subdomain string `json:"subdomain,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
}

const handshakeTimeout = 10 * time.Second

// WriteJSON writes a single JSON message with a deadline. Intended only for
// the handshake stream, which carries exactly one message per direction.
func WriteJSON(conn net.Conn, v any) error {
	conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetWriteDeadline(time.Time{})
	return json.NewEncoder(conn).Encode(v)
}

// ReadJSON reads a single JSON message with a deadline. The stream must not
// carry further data after the message: the decoder may buffer past it.
func ReadJSON(conn net.Conn, v any) error {
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetReadDeadline(time.Time{})
	return json.NewDecoder(bufio.NewReader(conn)).Decode(v)
}
