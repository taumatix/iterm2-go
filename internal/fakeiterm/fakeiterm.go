// Package fakeiterm is a stand-in for a running iTerm2, used by this module's
// tests.
//
// It is deliberately not a mock of [iterm2.Conn]'s internals: it listens on a
// real unix domain socket, performs a real WebSocket upgrade, validates the
// same handshake headers iTerm2 validates, and exchanges real protocol-buffer
// messages. A test driving it therefore exercises the whole client — dialling,
// the handshake, framing, id matching, notification fan-out — rather than the
// parts of it in isolation.
//
// What it cannot do is prove this module agrees with iTerm2 itself, because the
// expectations here were written from api.proto and iTerm2's source rather than
// observed from the app. TestLive covers that, and only runs when a real iTerm2
// with the API enabled is present.
package fakeiterm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// Subprotocol is the WebSocket subprotocol iTerm2 requires. It is repeated here
// rather than imported so that a change to the client's constant shows up as a
// failing handshake instead of silently agreeing with itself.
const Subprotocol = "api.iterm2.com"

// Handler answers one request. Returning nil sends nothing back, which is how a
// test simulates iTerm2 ignoring a request.
type Handler func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage

// Handshake records what a client sent on the upgrade request, so a test can
// assert on headers iTerm2 would have validated.
type Handshake struct {
	Cookie         string
	Key            string
	LibraryVersion string
	AdvisoryName   string
	Origin         string
	DisableAuthUI  string
	Subprotocols   []string
}

// Server is a fake iTerm2 listening on a unix domain socket.
type Server struct {
	// SocketPath is the socket to point [iterm2.WithSocketPath] at.
	SocketPath string

	// dir holds the socket and is removed by Close.
	dir string

	listener net.Listener
	http     *http.Server

	// cookie, when set, must be presented by the client or the upgrade is
	// refused with 401 — the same way iTerm2 refuses an unauthorised client.
	cookie string

	mu         sync.Mutex
	handler    Handler
	handshakes []Handshake
	conns      []*websocket.Conn
	connected  chan struct{}

	closeOnce sync.Once
	done      chan struct{}
}

// Option configures [Start].
type Option func(*Server)

// RequireCookie makes the server refuse any upgrade that does not present
// cookie, answering 401 as iTerm2 does.
func RequireCookie(cookie string) Option {
	return func(s *Server) { s.cookie = cookie }
}

// WithHandler sets the request handler. The default echoes an empty response
// carrying the request's id, which is enough for tests that only care about
// framing.
func WithHandler(h Handler) Option {
	return func(s *Server) { s.handler = h }
}

// Start brings up a fake iTerm2 on a unix socket. The caller must Close it,
// which also removes the socket and its directory.
//
// It chooses the directory itself rather than taking one, because the path
// budget is tight enough to be a trap: sockaddr_un.sun_path is 104 bytes on
// Darwin and 108 on Linux, and t.TempDir() spends a lot of it — on a macOS
// runner it expands to /var/folders/<32 chars>/T/<test name>/001, which put this
// over the limit for the longer test names while Linux's short /tmp paths passed.
// CI caught that on the first run; taking the choice away from the caller is
// what stops it coming back.
func Start(opts ...Option) (*Server, error) {
	dir, err := os.MkdirTemp("", "fakeiterm")
	if err != nil {
		return nil, fmt.Errorf("fakeiterm: making a socket directory: %w", err)
	}

	s := &Server{
		// A one-character name, because every byte here is part of that budget.
		SocketPath: filepath.Join(dir, "s"),
		dir:        dir,
		connected:  make(chan struct{}, 16),
		done:       make(chan struct{}),
		handler:    echoHandler,
	}
	for _, opt := range opts {
		opt(s)
	}

	// Still asserted: TMPDIR is the caller's to set, and a long one would fail at
	// bind with "invalid argument", which says nothing about why.
	if len(s.SocketPath) > 100 {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("fakeiterm: socket path is %d bytes, too long for a unix socket (set TMPDIR to something shorter): %s", len(s.SocketPath), s.SocketPath)
	}

	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("fakeiterm: listening on %s: %w", s.SocketPath, err)
	}
	s.listener = ln
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve)}

	go func() {
		// A closed listener is the ordinary way this returns, so it is not news.
		_ = s.http.Serve(ln)
	}()
	return s, nil
}

// SetHandler replaces the request handler. It is safe to call while a client is
// connected.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Handshakes returns the upgrade requests seen so far, oldest first.
func (s *Server) Handshakes() []Handshake {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Handshake(nil), s.handshakes...)
}

// WaitForConnection blocks until a client has completed the upgrade, or the
// timeout expires.
func (s *Server) WaitForConnection(timeout time.Duration) error {
	select {
	case <-s.connected:
		return nil
	case <-time.After(timeout):
		return errors.New("fakeiterm: no client connected before the timeout")
	}
}

// Notify pushes an unprompted notification to every connected client, which is
// the one thing iTerm2 sends without being asked.
func (s *Server) Notify(ctx context.Context, n *apipb.Notification) error {
	msg := &apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_Notification{Notification: n},
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()

	if len(conns) == 0 {
		return errors.New("fakeiterm: no client to notify")
	}
	for _, c := range conns {
		if err := c.Write(ctx, websocket.MessageBinary, payload); err != nil {
			return err
		}
	}
	return nil
}

// SendRaw writes bytes straight onto every connection, bypassing protobuf
// encoding. It exists so a test can hand the client something malformed.
func (s *Server) SendRaw(ctx context.Context, typ websocket.MessageType, payload []byte) error {
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()

	if len(conns) == 0 {
		return errors.New("fakeiterm: no client to write to")
	}
	for _, c := range conns {
		if err := c.Write(ctx, typ, payload); err != nil {
			return err
		}
	}
	return nil
}

// CloseConnections drops every client connection without a close frame,
// simulating iTerm2 quitting.
func (s *Server) CloseConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.CloseNow()
	}
}

// Close shuts the server down and removes its socket.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		s.CloseConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = s.http.Shutdown(ctx)
		// Removes the socket along with the directory Start made for it.
		_ = os.RemoveAll(s.dir)
	})
	return err
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	hs := Handshake{
		Cookie:         r.Header.Get("x-iterm2-cookie"),
		Key:            r.Header.Get("x-iterm2-key"),
		LibraryVersion: r.Header.Get("x-iterm2-library-version"),
		AdvisoryName:   r.Header.Get("x-iterm2-advisory-name"),
		Origin:         r.Header.Get("Origin"),
		DisableAuthUI:  r.Header.Get("x-iterm2-disable-auth-ui"),
		Subprotocols:   requestedSubprotocols(r),
	}
	s.mu.Lock()
	s.handshakes = append(s.handshakes, hs)
	s.mu.Unlock()

	if s.cookie != "" && hs.Cookie != s.cookie {
		// iTerm2 answers 401 for a missing or spent cookie, and the client turns
		// that into ErrUnauthorized.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{Subprotocol},
	})
	if err != nil {
		return
	}
	if c.Subprotocol() != Subprotocol {
		c.Close(websocket.StatusProtocolError, "subprotocol")
		return
	}
	c.SetReadLimit(64 << 20)

	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	select {
	case s.connected <- struct{}{}:
	default:
	}

	s.readLoop(c)
}

func (s *Server) readLoop(c *websocket.Conn) {
	for {
		typ, payload, err := c.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			c.Close(websocket.StatusUnsupportedData, "want binary")
			return
		}

		var req apipb.ClientOriginatedMessage
		if err := proto.Unmarshal(payload, &req); err != nil {
			c.Close(websocket.StatusInvalidFramePayloadData, "bad protobuf")
			return
		}

		s.mu.Lock()
		h := s.handler
		s.mu.Unlock()

		resp := h(&req)
		if resp == nil {
			continue
		}
		// The id is the server's responsibility: a handler written by a test
		// should not have to remember to copy it, and forgetting to would look
		// like a client bug.
		if resp.Id == nil && req.Id != nil {
			resp.Id = proto.Int64(req.GetId())
		}
		out, err := proto.Marshal(resp)
		if err != nil {
			return
		}
		if err := c.Write(context.Background(), websocket.MessageBinary, out); err != nil {
			return
		}
	}
}

// requestedSubprotocols reads the subprotocols the client offered.
// coder/websocket exports no helper for this, and the header is a
// comma-separated list per RFC 6455 section 4.1.
func requestedSubprotocols(r *http.Request) []string {
	var out []string
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, field := range strings.Split(header, ",") {
			if field = strings.TrimSpace(field); field != "" {
				out = append(out, field)
			}
		}
	}
	return out
}

// echoHandler answers every request with an empty response, which the server
// then stamps with the request's id.
func echoHandler(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
	return &apipb.ServerOriginatedMessage{}
}
