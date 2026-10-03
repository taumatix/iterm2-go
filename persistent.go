package iterm2

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// ErrReconnecting is returned by a [Persistent] connection while it has lost
// iTerm2 and is dialling again. It wraps [ErrClosed], so code that already
// checks for a closed connection keeps working.
var ErrReconnecting = fmt.Errorf("iterm2: reconnecting to iTerm2: %w", ErrClosed)

// Reconnect reports that a [Persistent] connection was replaced.
//
// Notifications iTerm2 posted between the old connection ending and the new
// one subscribing are gone, and nothing replays them. Treat a Reconnect as "my
// view of iTerm2 may be stale": resynchronise, for instance with
// [Conn.ListSessions]. It is sent only once every subscription is back, so a
// change made after the resynchronisation is delivered.
type Reconnect struct {
	// Count is how many reconnects this Persistent has made, this one included.
	// The channel keeps only the latest, so a caller that fell behind sees the
	// count jump rather than every event.
	Count int
	// Cause is why the previous connection ended. It wraps ErrClosed.
	Cause error
	// At is when the new connection was ready.
	At time.Time
}

// Persistent is a connection to iTerm2 that survives iTerm2 restarting, which
// it does on every update.
//
// A [Conn] cannot come back: its cookie is spent. Persistent dials a fresh one
// when the current one ends, re-makes every [DurableSubscription] on it, and
// reports each replacement on [Persistent.Reconnects]. Between the two, calls
// return [ErrReconnecting].
//
// It is the loop the README used to ask every program to write.
type Persistent struct {
	opts             []Option
	minWait, maxWait time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// resub serialises Subscribe against a reconnect re-making subscriptions,
	// so a subscription is either made on the old connection and re-made on
	// the new one, or made on the new one — never left on a dead one.
	resub sync.Mutex

	mu     sync.Mutex
	conn   *Conn
	closed bool
	count  int
	subs   map[*DurableSubscription]struct{}

	reconnects chan Reconnect
}

// WithReconnectBackoff sets how long a [Persistent] connection waits between
// attempts while iTerm2 is away: min after the first failure, doubling to max.
// The default is one second to thirty. [Connect] ignores it.
func WithReconnectBackoff(min, max time.Duration) Option {
	return func(c *config) { c.reconnectMin, c.reconnectMax = min, max }
}

// ConnectPersistent connects to iTerm2 as [Connect] does, with the same
// options, and keeps reconnecting with them until [Persistent.Close].
//
// ctx bounds the first connection only. Its error is returned as Connect's
// would be, so a program started with the API switched off finds out at once.
// Every attempt obtains fresh credentials from the configured source, which
// with the default credentials means asking iTerm2 for a new cookie.
func ConnectPersistent(ctx context.Context, opts ...Option) (*Persistent, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.creds == nil {
		// One source for every attempt, rather than a new default per Connect,
		// so where its last cookie came from is remembered between them.
		opts = append(append([]Option(nil), opts...), WithCredentialSource(DefaultCredentials(cfg.advisoryName)))
	}
	minWait, maxWait := cfg.reconnectMin, cfg.reconnectMax
	if minWait <= 0 {
		minWait = time.Second
	}
	if maxWait < minWait {
		maxWait = max(30*time.Second, minWait)
	}

	conn, err := Connect(ctx, opts...)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(context.Background())
	p := &Persistent{
		opts: opts, minWait: minWait, maxWait: maxWait,
		ctx: pctx, cancel: cancel, done: make(chan struct{}),
		conn:       conn,
		subs:       make(map[*DurableSubscription]struct{}),
		reconnects: make(chan Reconnect, 1),
	}
	go p.run()
	return p, nil
}

// Reconnects delivers a [Reconnect] each time the connection is replaced. It
// holds only the latest; see [Reconnect.Count].
func (p *Persistent) Reconnects() <-chan Reconnect { return p.reconnects }

// Conn returns the current connection, for the typed methods on [Conn]. It
// returns [ErrReconnecting] while iTerm2 is away and [ErrClosed] after Close.
// Do not keep the result: it is replaced at the next reconnect.
func (p *Persistent) Conn() (*Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.closed:
		return nil, ErrClosed
	case p.conn == nil:
		return nil, ErrReconnecting
	}
	return p.conn, nil
}

// Do sends one request on the current connection. See [Conn.Do].
func (p *Persistent) Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error) {
	conn, err := p.Conn()
	if err != nil {
		return nil, err
	}
	return conn.Do(ctx, req)
}

// Close stops reconnecting, closes the current connection and every
// [DurableSubscription]. Calling it twice is harmless.
func (p *Persistent) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.done
		return nil
	}
	p.closed = true
	conn := p.conn
	subs := p.subs
	p.subs = map[*DurableSubscription]struct{}{}
	p.mu.Unlock()

	p.cancel()
	var err error
	if conn != nil {
		err = conn.Close()
	}
	<-p.done
	for d := range subs {
		d.end(nil)
	}
	return err
}

func (p *Persistent) run() {
	defer close(p.done)
	for {
		p.mu.Lock()
		conn := p.conn
		p.mu.Unlock()

		select {
		case <-p.ctx.Done():
			return
		case <-conn.Done():
		}
		cause := conn.Err()
		p.mu.Lock()
		p.conn = nil
		p.mu.Unlock()

		next, ok := p.redial()
		if !ok {
			return
		}

		p.resub.Lock()
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			p.resub.Unlock()
			_ = next.Close()
			return
		}
		p.conn = next
		p.count++
		count := p.count
		subs := make([]*DurableSubscription, 0, len(p.subs))
		for d := range p.subs {
			subs = append(subs, d)
		}
		p.mu.Unlock()
		for _, d := range subs {
			d.resubscribe(p.ctx, next)
		}
		p.resub.Unlock()

		p.announce(Reconnect{Count: count, Cause: cause, At: time.Now()})
	}
}

// redial tries until a connection succeeds or the Persistent is closed.
func (p *Persistent) redial() (*Conn, bool) {
	wait := p.minWait
	for {
		conn, err := Connect(p.ctx, p.opts...)
		if err == nil {
			return conn, true
		}
		select {
		case <-p.ctx.Done():
			return nil, false
		case <-time.After(wait):
		}
		wait = min(wait*2, p.maxWait)
	}
}

// announce replaces any unread Reconnect with r, so the channel never blocks
// the reconnect loop and always holds the latest.
func (p *Persistent) announce(r Reconnect) {
	for {
		select {
		case p.reconnects <- r:
			return
		default:
		}
		select {
		case <-p.reconnects:
		default:
		}
	}
}

// DurableSubscription is a [Subscription] that a [Persistent] connection re-makes
// on every new connection, so [DurableSubscription.C] stays open across
// iTerm2 restarts. What iTerm2 posted while it was away is lost; a
// [Reconnect] says when that happened.
type DurableSubscription struct {
	p   *Persistent
	req *apipb.NotificationRequest
	ch  chan *apipb.Notification

	dropped atomic.Uint64

	mu     sync.Mutex
	cur    *Subscription
	closed bool
	err    error
}

// C is the channel notifications arrive on. It is closed by
// [DurableSubscription.Unsubscribe], by [Persistent.Close], or when the
// subscription cannot be re-made after a reconnect, in which case
// [DurableSubscription.Err] says why.
func (d *DurableSubscription) C() <-chan *apipb.Notification { return d.ch }

// Err is nil until the channel closes, and nil after an Unsubscribe or a Close.
// It is set when iTerm2 refused the subscription on a new connection — a
// per-session subscription whose session did not survive the restart, for one.
func (d *DurableSubscription) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// Dropped counts notifications discarded because C was full, across every
// connection this subscription has had.
func (d *DurableSubscription) Dropped() uint64 { return d.dropped.Load() }

// Unsubscribe stops the subscription for good: it is not re-made at the next
// reconnect, and C is closed.
func (d *DurableSubscription) Unsubscribe(ctx context.Context) error {
	d.p.mu.Lock()
	delete(d.p.subs, d)
	d.p.mu.Unlock()

	d.mu.Lock()
	cur := d.cur
	d.mu.Unlock()
	d.end(nil)
	if cur == nil {
		return nil
	}
	return cur.Unsubscribe(ctx)
}

// Subscribe asks iTerm2 to post notifications, as [Conn.Subscribe] does, and
// re-asks on every new connection.
func (p *Persistent) Subscribe(ctx context.Context, req *apipb.NotificationRequest) (*DurableSubscription, error) {
	p.resub.Lock()
	defer p.resub.Unlock()

	conn, err := p.Conn()
	if err != nil {
		return nil, err
	}
	sub, err := conn.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	d := &DurableSubscription{
		p:   p,
		req: proto.Clone(req).(*apipb.NotificationRequest),
		ch:  make(chan *apipb.Notification, conn.subscribeBuf),
		cur: sub,
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = sub.Unsubscribe(ctx)
		return nil, ErrClosed
	}
	p.subs[d] = struct{}{}
	p.mu.Unlock()
	go d.pump(sub)
	return d, nil
}

// SubscribeNewSessions is [Conn.SubscribeNewSessions] on a Persistent.
func (p *Persistent) SubscribeNewSessions(ctx context.Context) (*DurableSubscription, error) {
	return p.Subscribe(ctx, &apipb.NotificationRequest{NotificationType: apipb.NotificationType_NOTIFY_ON_NEW_SESSION.Enum()})
}

// SubscribeTerminatedSessions is [Conn.SubscribeTerminatedSessions] on a Persistent.
func (p *Persistent) SubscribeTerminatedSessions(ctx context.Context) (*DurableSubscription, error) {
	return p.Subscribe(ctx, &apipb.NotificationRequest{NotificationType: apipb.NotificationType_NOTIFY_ON_TERMINATE_SESSION.Enum()})
}

// SubscribeLayoutChanges is [Conn.SubscribeLayoutChanges] on a Persistent.
func (p *Persistent) SubscribeLayoutChanges(ctx context.Context) (*DurableSubscription, error) {
	return p.Subscribe(ctx, &apipb.NotificationRequest{NotificationType: apipb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE.Enum()})
}

// SubscribeFocusChanges is [Conn.SubscribeFocusChanges] on a Persistent.
func (p *Persistent) SubscribeFocusChanges(ctx context.Context) (*DurableSubscription, error) {
	return p.Subscribe(ctx, &apipb.NotificationRequest{NotificationType: apipb.NotificationType_NOTIFY_ON_FOCUS_CHANGE.Enum()})
}

// SubscribeVariableChanges is [Conn.SubscribeVariableChanges] on a Persistent.
func (p *Persistent) SubscribeVariableChanges(ctx context.Context, scope VariableScope, identifier, name string) (*DurableSubscription, error) {
	return p.Subscribe(ctx, variableChangesRequest(scope, identifier, name))
}

// resubscribeTimeout bounds re-making one subscription on a new connection.
const resubscribeTimeout = 10 * time.Second

func (d *DurableSubscription) resubscribe(ctx context.Context, conn *Conn) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, resubscribeTimeout)
	defer cancel()
	sub, err := conn.Subscribe(ctx, d.req)
	if err != nil {
		d.p.mu.Lock()
		delete(d.p.subs, d)
		d.p.mu.Unlock()
		d.end(fmt.Errorf("iterm2: re-subscribing after a reconnect: %w", err))
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = sub.Unsubscribe(ctx)
		return
	}
	d.cur = sub
	d.mu.Unlock()
	go d.pump(sub)
}

// pump forwards one connection's notifications until that connection's
// subscription ends. A reconnect starts the next pump.
func (d *DurableSubscription) pump(sub *Subscription) {
	for n := range sub.C() {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		select {
		case d.ch <- n:
		default:
			d.dropped.Add(1)
		}
		d.mu.Unlock()
	}
}

// end closes C once, recording err if it is the first reason given.
func (d *DurableSubscription) end(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	d.err = err
	close(d.ch)
}
