package iterm2

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// maxMessageBytes caps an incoming message. A screen-contents response over a
// long scrollback is the biggest thing iTerm2 sends; iTerm2's Python library
// lifts the limit entirely, and 64 MiB is generous for that while still bounded.
const maxMessageBytes = 64 << 20

// Conn is a connection to a running iTerm2.
//
// A Conn is safe for concurrent use: requests from many goroutines are
// serialised onto the socket and matched to their responses by message id.
//
// Every method takes a context. Cancelling one abandons that request only — a
// response arriving afterwards is discarded. To tear the connection down, call
// [Conn.Close].
type Conn struct {
	ws           *websocket.Conn
	advisoryName string
	subscribeBuf int

	// writeMu serialises writes: the WebSocket library permits one writer at a
	// time, and a request is one message.
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *apipb.ServerOriginatedMessage
	subs    map[*Subscription]struct{}
	closing bool
	cause   error

	closeOnce sync.Once
	readDone  chan struct{}
}

type config struct {
	socketPath   string
	tcpAddress   string
	forceTCP     bool
	creds        CredentialSource
	advisoryName string
	subscribeBuf int

	// reconnectMin and reconnectMax are read by ConnectPersistent only.
	reconnectMin, reconnectMax time.Duration
}

// Option configures [Connect].
type Option func(*config)

// WithSocketPath overrides the unix domain socket to connect to. The default is
// [DefaultSocketPath].
func WithSocketPath(path string) Option {
	return func(c *config) { c.socketPath = path }
}

// WithTCPAddress overrides the loopback address used when no unix socket is
// present. The default is [LegacyTCPAddress].
func WithTCPAddress(addr string) Option {
	return func(c *config) { c.tcpAddress = addr }
}

// ForceTCP connects over loopback TCP even when a unix socket exists.
func ForceTCP() Option {
	return func(c *config) { c.forceTCP = true }
}

// WithCredentials supplies a cookie and key directly, instead of reading the
// environment or asking iTerm2 over AppleScript. Use it when something else in
// the program already holds a cookie, and in tests.
func WithCredentials(cookie, key string) Option {
	return WithCredentialSource(StaticCredentials(cookie, key))
}

// WithCredentialSource replaces how credentials are obtained. The default is
// [DefaultCredentials].
func WithCredentialSource(src CredentialSource) Option {
	return func(c *config) { c.creds = src }
}

// WithAdvisoryName sets the name iTerm2 shows for this client in its script
// console and its authorisation prompt. It should name the program, not this
// library. The default is the running program's filename.
func WithAdvisoryName(name string) Option {
	return func(c *config) { c.advisoryName = name }
}

// WithSubscriptionBuffer sets how many notifications a [Subscription] buffers
// before it starts dropping them. The default is 64.
//
// Notifications are dropped rather than blocking, because iTerm2 posts screen
// updates and keystrokes faster than most consumers read them, and a blocked
// reader would stall every other request on the connection. See
// [Subscription.Dropped].
func WithSubscriptionBuffer(n int) Option {
	return func(c *config) { c.subscribeBuf = n }
}

// Connect dials a running iTerm2 and completes the API handshake.
//
// It prefers the unix domain socket at [DefaultSocketPath] and falls back to
// [LegacyTCPAddress] when that socket does not exist, matching iTerm2's own
// Python library.
//
// The API must be enabled in iTerm2 (Settings > General > Magic > Enable Python
// API) or the handshake fails with an error wrapping [ErrUnauthorized].
func Connect(ctx context.Context, opts ...Option) (*Conn, error) {
	cfg := config{
		socketPath:   DefaultSocketPath(),
		tcpAddress:   LegacyTCPAddress,
		subscribeBuf: 64,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.creds == nil {
		cfg.creds = DefaultCredentials(cfg.advisoryName)
	}
	if cfg.subscribeBuf < 1 {
		cfg.subscribeBuf = 1
	}

	ws, err := dialWithCredentials(ctx, cfg)
	retrier, canRetry := cfg.creds.(interface{ retryAfterUnauthorized() bool })
	if errors.Is(err, ErrUnauthorized) && canRetry && retrier.retryAfterUnauthorized() {
		// The default source's environment cookie was refused; its next answer
		// is a fresh cookie over AppleScript. Once, as iTerm2's Python library does.
		ws, err = dialWithCredentials(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxMessageBytes)

	c := &Conn{
		ws:           ws,
		advisoryName: cfg.advisoryName,
		subscribeBuf: cfg.subscribeBuf,
		pending:      make(map[int64]chan *apipb.ServerOriginatedMessage),
		subs:         make(map[*Subscription]struct{}),
		readDone:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// dialWithCredentials asks the source for credentials and completes one
// handshake with them. It is one attempt: a cookie is spent by the attempt
// whether or not the attempt succeeds.
func dialWithCredentials(ctx context.Context, cfg config) (*websocket.Conn, error) {
	creds, err := cfg.creds.Credentials(ctx)
	if err != nil {
		return nil, err
	}

	network, address := "unix", cfg.socketPath
	if cfg.forceTCP || !socketExists(cfg.socketPath) {
		network, address = "tcp", cfg.tcpAddress
	}
	return dialWebSocket(ctx, network, address, creds, cfg.advisoryName)
}

// Done is closed when the connection ends, whether through [Conn.Close] or
// because iTerm2 went away. [Conn.Err] then says which.
//
// A program that only listens has no request in flight to fail, so without this
// it cannot tell iTerm2 has quit. A connection cannot be revived — its cookie is
// spent — so the response to Done is a fresh [Connect]:
//
//	for {
//		conn, err := iterm2.Connect(ctx)
//		if err != nil { /* back off and retry */ }
//		// subscribe to what you need; notifications posted while
//		// disconnected are gone, so resynchronise any state here
//		<-conn.Done()
//	}
func (c *Conn) Done() <-chan struct{} { return c.readDone }

// Err is nil while the connection is up. Once [Conn.Done] is closed it reports
// why: [ErrClosed] after [Conn.Close], or the error that ended the connection
// when iTerm2 dropped it.
func (c *Conn) Err() error {
	select {
	case <-c.readDone:
		return c.closeCause()
	default:
		return nil
	}
}

// socketExists reports whether path is a socket we could connect to. A plain
// file at that path is not one, and treating it as one would turn a clear
// "no socket, falling back to TCP" into a confusing dial error.
func socketExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// Close shuts the connection down and waits for its reader to finish.
//
// Requests in flight fail with [ErrClosed], and every [Subscription] channel is
// closed. Calling it more than once is harmless, and calling it on a connection
// iTerm2 already dropped returns nil.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()

		err = c.ws.Close(websocket.StatusNormalClosure, "")
		// A connection iTerm2 already dropped is closed as far as the caller is
		// concerned, so the close frame failing to send is not news.
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	})
	<-c.readDone
	return err
}

// Do sends one request and returns iTerm2's response to it.
//
// This is the whole API: every request type in api.proto is a field of
// [apipb.ClientOriginatedMessage], and the typed methods on Conn are built on
// this one. req is not modified — Do sends a copy carrying the message id.
//
// A response whose `error` field is set comes back as an [*APIError], because
// iTerm2 sets it only for a request it could not parse. Per-request status
// enums are left in the response for the caller to read; the typed methods turn
// a non-OK status into a [*StatusError].
func (c *Conn) Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error) {
	if req == nil {
		return nil, errors.New("iterm2: nil request")
	}

	out := proto.Clone(req).(*apipb.ClientOriginatedMessage)
	reply := make(chan *apipb.ServerOriginatedMessage, 1)

	c.mu.Lock()
	if c.closing || c.cause != nil {
		c.mu.Unlock()
		return nil, c.closeCause()
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = reply
	c.mu.Unlock()

	out.Id = proto.Int64(id)

	if err := c.write(ctx, out); err != nil {
		c.forget(id)
		return nil, err
	}

	select {
	case resp, ok := <-reply:
		if !ok {
			return nil, c.closeCause()
		}
		if text := resp.GetError(); text != "" {
			return resp, &APIError{Message: text}
		}
		return resp, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	case <-c.readDone:
		return nil, c.closeCause()
	}
}

func (c *Conn) write(ctx context.Context, msg *apipb.ClientOriginatedMessage) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("iterm2: encoding request: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.Write(ctx, websocket.MessageBinary, payload); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.closeCause() != nil {
			return c.closeCause()
		}
		return fmt.Errorf("iterm2: sending request: %w", err)
	}
	return nil
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// closeCause reports why the connection is unusable, or nil while it is fine.
func (c *Conn) closeCause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.cause != nil:
		return c.cause
	case c.closing:
		return ErrClosed
	default:
		return nil
	}
}

// readLoop owns every read on the socket. It exits on the first read error —
// which is also how a deliberate Close is seen from here — and then fails every
// waiting request and closes every subscription, so nothing is left hanging.
func (c *Conn) readLoop() {
	defer close(c.readDone)

	for {
		typ, payload, err := c.ws.Read(context.Background())
		if err != nil {
			c.shutdown(fmt.Errorf("iterm2: reading from iTerm2: %w", err))
			return
		}
		if typ != websocket.MessageBinary {
			// iTerm2 only ever sends binary. A text frame means we are not
			// talking to iTerm2, or that it has changed; guessing at the payload
			// would be worse than saying so.
			c.shutdown(fmt.Errorf("iterm2: peer sent a %v frame, want binary", typ))
			return
		}

		var msg apipb.ServerOriginatedMessage
		if err := proto.Unmarshal(payload, &msg); err != nil {
			c.shutdown(fmt.Errorf("iterm2: decoding %d-byte message: %w", len(payload), err))
			return
		}
		c.dispatch(&msg)
	}
}

// dispatch routes one decoded message.
//
// A notification is the only thing iTerm2 sends unprompted, and it arrives with
// no id (api.proto, ServerOriginatedMessage.notification). Everything else is a
// response to an outstanding request. A message that is neither — no id and no
// notification — is dropped rather than treated as fatal, so a future iTerm2
// that sends something new does not break this one.
func (c *Conn) dispatch(msg *apipb.ServerOriginatedMessage) {
	if n := msg.GetNotification(); n != nil {
		c.mu.Lock()
		subs := make([]*Subscription, 0, len(c.subs))
		for s := range c.subs {
			subs = append(subs, s)
		}
		c.mu.Unlock()
		for _, s := range subs {
			s.deliver(n)
		}
		return
	}

	if msg.Id == nil {
		return
	}

	c.mu.Lock()
	reply, ok := c.pending[msg.GetId()]
	delete(c.pending, msg.GetId())
	c.mu.Unlock()
	if ok {
		// Buffered with room for one, and taken out of the map above, so this
		// cannot block and cannot race with shutdown closing the same channel.
		reply <- msg
	}
}

func (c *Conn) shutdown(cause error) {
	c.mu.Lock()
	if c.cause == nil {
		// A deliberate Close reaches here as a read error too. Reporting that as
		// ErrClosed rather than as whatever the socket said spares callers from
		// having to recognise a transport-level close.
		if c.closing {
			cause = ErrClosed
		} else {
			// ErrClosed is documented as what every operation returns once iTerm2
			// has gone, and until v0.2.0 a drop returned only the read error, so
			// errors.Is(err, ErrClosed) missed the case it was written for. Wrap
			// both: callers can test for the drop and still read why.
			cause = fmt.Errorf("%w: %w", ErrClosed, cause)
		}
		c.cause = cause
	}
	pending := c.pending
	c.pending = make(map[int64]chan *apipb.ServerOriginatedMessage)
	subs := c.subs
	c.subs = make(map[*Subscription]struct{})
	c.mu.Unlock()

	for _, reply := range pending {
		close(reply)
	}
	for s := range subs {
		s.shutdown()
	}
}
