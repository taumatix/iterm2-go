package iterm2

import (
	"context"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// Subscription delivers one kind of notification from iTerm2.
//
// iTerm2 posts every notification a connection has subscribed to down the one
// socket, with nothing tying a notification back to the request that asked for
// it. A Subscription therefore remembers what it asked for and delivers only
// the notifications that match, so two subscriptions on one Conn do not see
// each other's traffic.
//
// iTerm2 refuses a second subscription to the same notification type and
// session on one connection, answering ALREADY_SUBSCRIBED; [Conn.Subscribe]
// surfaces that as a [*StatusError]. Fan one Subscription out in your own code,
// or use a second Conn.
type Subscription struct {
	conn    *Conn
	kind    apipb.NotificationType
	session string

	ch      chan *apipb.Notification
	dropped atomic.Uint64

	// sendMu serialises delivering a notification against closing the channel.
	//
	// [Conn.dispatch] copies the subscription set under the connection's mutex and
	// then releases it before delivering, so an Unsubscribe from another goroutine
	// can close this channel while the read loop is between the two. Sending on a
	// closed channel panics, and it would panic on the read loop's goroutine —
	// taking the whole program down rather than just this subscription.
	sendMu sync.Mutex
	closed bool

	closeOnce sync.Once
}

// C is the channel notifications arrive on. It is closed when the subscription
// ends, whether through [Subscription.Unsubscribe] or because the connection
// went away.
func (s *Subscription) C() <-chan *apipb.Notification { return s.ch }

// Dropped counts notifications discarded because the buffer was full. A
// non-zero value means the consumer is slower than iTerm2 is posting; see
// [WithSubscriptionBuffer].
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Unsubscribe tells iTerm2 to stop posting this notification and closes
// [Subscription.C].
//
// The channel is closed even when the request fails — there is nothing left to
// deliver either way — so the returned error reports what iTerm2 said and is
// safe to log rather than handle. Calling it twice is harmless.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	var err error
	s.closeOnce.Do(func() {
		s.conn.mu.Lock()
		delete(s.conn.subs, s)
		dead := s.conn.cause != nil || s.conn.closing
		s.conn.mu.Unlock()
		s.closeChannel()

		if dead {
			return
		}
		req := s.request(false)
		var resp *apipb.ServerOriginatedMessage
		resp, err = s.conn.Do(ctx, &apipb.ClientOriginatedMessage{
			Submessage: &apipb.ClientOriginatedMessage_NotificationRequest{NotificationRequest: req},
		})
		if err != nil {
			return
		}
		err = checkStatus("Unsubscribe", resp.GetNotificationResponse().GetStatus())
	})
	return err
}

// request rebuilds the NotificationRequest for this subscription. Unsubscribing
// has to name the same type and session as subscribing did.
func (s *Subscription) request(subscribe bool) *apipb.NotificationRequest {
	req := &apipb.NotificationRequest{
		Subscribe:        proto.Bool(subscribe),
		NotificationType: s.kind.Enum(),
	}
	if s.session != "" {
		req.Session = proto.String(s.session)
	}
	return req
}

// deliver hands n to the consumer if it matches what this subscription asked
// for. It never blocks: the read loop calls it, so a slow consumer would
// otherwise stall every request on the connection.
func (s *Subscription) deliver(n *apipb.Notification) {
	if !s.matches(n) {
		return
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// A subscription that has ended drops what is still in flight rather than
	// panicking on its closed channel. Holding the mutex across a non-blocking
	// send costs nothing: the only other holder is closeChannel.
	if s.closed {
		return
	}
	select {
	case s.ch <- n:
	default:
		s.dropped.Add(1)
	}
}

// closeChannel closes [Subscription.C] exactly once, shutting delivery down
// first so the read loop cannot be mid-send.
func (s *Subscription) closeChannel() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

func (s *Subscription) shutdown() {
	s.closeOnce.Do(s.closeChannel)
}

// matches reports whether n is one of the notifications this subscription asked
// for.
//
// The mapping from NotificationType to the field iTerm2 sets in Notification is
// positional in api.proto but not derivable from it, so it is written out. A
// type this package does not know about matches nothing rather than everything:
// delivering the wrong notifications is worse than delivering none, and
// [Conn.Subscribe] rejects unknown types up front so this stays unreachable.
func (s *Subscription) matches(n *apipb.Notification) bool {
	session := ""
	switch s.kind {
	case apipb.NotificationType_NOTIFY_ON_KEYSTROKE:
		if n.KeystrokeNotification == nil {
			return false
		}
		session = n.GetKeystrokeNotification().GetSession()
	case apipb.NotificationType_NOTIFY_ON_SCREEN_UPDATE:
		if n.ScreenUpdateNotification == nil {
			return false
		}
		session = n.GetScreenUpdateNotification().GetSession()
	case apipb.NotificationType_NOTIFY_ON_PROMPT:
		if n.PromptNotification == nil {
			return false
		}
		session = n.GetPromptNotification().GetSession()
	case apipb.NotificationType_NOTIFY_ON_CUSTOM_ESCAPE_SEQUENCE:
		if n.CustomEscapeSequenceNotification == nil {
			return false
		}
		session = n.GetCustomEscapeSequenceNotification().GetSession()
	case apipb.NotificationType_NOTIFY_ON_LOCATION_CHANGE:
		// Deprecated in api.proto, and handled anyway: an iTerm2 that still posts
		// it should be delivered rather than silently filtered out. Watch the
		// "path", "hostname" and "username" variables instead — see
		// [Conn.SubscribeVariableChanges].
		if n.LocationChangeNotification == nil { //nolint:staticcheck // upstream deprecation
			return false
		}
		session = n.GetLocationChangeNotification().GetSession() //nolint:staticcheck // upstream deprecation
	case apipb.NotificationType_NOTIFY_ON_VARIABLE_CHANGE:
		// Scoped by the VariableMonitorRequest rather than by the session field,
		// so the per-session filter below does not apply.
		return n.VariableChangedNotification != nil
	case apipb.NotificationType_NOTIFY_ON_NEW_SESSION:
		return n.NewSessionNotification != nil
	case apipb.NotificationType_NOTIFY_ON_TERMINATE_SESSION:
		return n.TerminateSessionNotification != nil
	case apipb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE:
		return n.LayoutChangedNotification != nil
	case apipb.NotificationType_NOTIFY_ON_FOCUS_CHANGE:
		return n.FocusChangedNotification != nil
	case apipb.NotificationType_NOTIFY_ON_SERVER_ORIGINATED_RPC:
		return n.ServerOriginatedRpcNotification != nil
	case apipb.NotificationType_NOTIFY_ON_BROADCAST_CHANGE:
		return n.BroadcastDomainsChanged != nil
	case apipb.NotificationType_NOTIFY_ON_PROFILE_CHANGE:
		return n.ProfileChangedNotification != nil
	default:
		return false
	}

	// A subscription for one session must not see another's. "all" and an unset
	// session both mean every session, which is how api.proto reads them.
	if s.session == "" || s.session == SessionAll {
		return true
	}
	return session == s.session
}

// Subscribe asks iTerm2 to post notifications and returns them on a channel.
//
// req must set NotificationType. Everything else api.proto allows — the session
// to watch, a VariableMonitorRequest, a KeystrokePattern filter — is passed
// through untouched, so this covers notification types this package has no
// named helper for. Subscribe sets Subscribe itself.
//
// The returned Subscription must be released with [Subscription.Unsubscribe],
// or iTerm2 keeps posting for the life of the connection.
func (c *Conn) Subscribe(ctx context.Context, req *apipb.NotificationRequest) (*Subscription, error) {
	if req == nil || req.NotificationType == nil {
		return nil, &APIError{Message: "Subscribe needs a NotificationType"}
	}
	kind := req.GetNotificationType()
	if kind == apipb.NotificationType_KEYSTROKE_FILTER {
		// api.proto: "Does not send a notification". Handing back a channel that
		// can never produce anything would be a trap.
		return nil, &APIError{Message: "KEYSTROKE_FILTER posts no notifications; send the request with Conn.Do"}
	}

	s := &Subscription{
		conn:    c,
		kind:    kind,
		session: req.GetSession(),
		ch:      make(chan *apipb.Notification, c.subscribeBuf),
	}
	if !s.matchesAnything() {
		return nil, &APIError{Message: "unknown NotificationType " + kind.String() + "; send the request with Conn.Do"}
	}

	// Registered before the request goes out, so a notification iTerm2 posts
	// between accepting the subscription and answering it is not lost.
	c.mu.Lock()
	if c.closing || c.cause != nil {
		c.mu.Unlock()
		return nil, c.closeCause()
	}
	c.subs[s] = struct{}{}
	c.mu.Unlock()

	out := proto.Clone(req).(*apipb.NotificationRequest)
	out.Subscribe = proto.Bool(true)

	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_NotificationRequest{NotificationRequest: out},
	})
	if err == nil {
		err = checkStatus("Subscribe", resp.GetNotificationResponse().GetStatus())
	}
	if err != nil {
		c.mu.Lock()
		delete(c.subs, s)
		c.mu.Unlock()
		s.closeOnce.Do(s.closeChannel)
		return nil, err
	}
	return s, nil
}

// matchesAnything reports whether matches knows this subscription's type at all.
// A type it does not know would silently deliver nothing, so Subscribe refuses
// it instead.
func (s *Subscription) matchesAnything() bool {
	switch s.kind {
	case apipb.NotificationType_NOTIFY_ON_KEYSTROKE,
		apipb.NotificationType_NOTIFY_ON_SCREEN_UPDATE,
		apipb.NotificationType_NOTIFY_ON_PROMPT,
		apipb.NotificationType_NOTIFY_ON_LOCATION_CHANGE,
		apipb.NotificationType_NOTIFY_ON_CUSTOM_ESCAPE_SEQUENCE,
		apipb.NotificationType_NOTIFY_ON_VARIABLE_CHANGE,
		apipb.NotificationType_NOTIFY_ON_NEW_SESSION,
		apipb.NotificationType_NOTIFY_ON_TERMINATE_SESSION,
		apipb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE,
		apipb.NotificationType_NOTIFY_ON_FOCUS_CHANGE,
		apipb.NotificationType_NOTIFY_ON_SERVER_ORIGINATED_RPC,
		apipb.NotificationType_NOTIFY_ON_BROADCAST_CHANGE,
		apipb.NotificationType_NOTIFY_ON_PROFILE_CHANGE:
		return true
	default:
		return false
	}
}

// SubscribeNewSessions posts a notification whenever a session is created, or a
// closed one is restored by undo. Read the id from
// [apipb.Notification.GetNewSessionNotification].
func (c *Conn) SubscribeNewSessions(ctx context.Context) (*Subscription, error) {
	return c.Subscribe(ctx, &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_NEW_SESSION.Enum(),
	})
}

// SubscribeTerminatedSessions posts a notification whenever a session ends.
func (c *Conn) SubscribeTerminatedSessions(ctx context.Context) (*Subscription, error) {
	return c.Subscribe(ctx, &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_TERMINATE_SESSION.Enum(),
	})
}

// SubscribeLayoutChanges posts a notification whenever windows, tabs or panes
// move. Each carries a full hierarchy snapshot, the same shape
// [Conn.ListSessions] returns.
func (c *Conn) SubscribeLayoutChanges(ctx context.Context) (*Subscription, error) {
	return c.Subscribe(ctx, &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE.Enum(),
	})
}

// SubscribeFocusChanges posts a notification when the application, window, tab
// or pane with focus changes. api.proto warns that duplicates are normal;
// ignore the ones that report no change.
func (c *Conn) SubscribeFocusChanges(ctx context.Context) (*Subscription, error) {
	return c.Subscribe(ctx, &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_FOCUS_CHANGE.Enum(),
	})
}

// SubscribeVariableChanges posts a notification when a variable changes.
//
// identifier names the session, tab or window to watch and is ignored for
// [ScopeApp]. name is the variable, such as "jobName" or "user.myVariable".
func (c *Conn) SubscribeVariableChanges(ctx context.Context, scope VariableScope, identifier, name string) (*Subscription, error) {
	return c.Subscribe(ctx, variableChangesRequest(scope, identifier, name))
}

func variableChangesRequest(scope VariableScope, identifier, name string) *apipb.NotificationRequest {
	monitor := &apipb.VariableMonitorRequest{
		Name:  proto.String(name),
		Scope: apipb.VariableScope(scope).Enum(),
	}
	if identifier != "" {
		monitor.Identifier = proto.String(identifier)
	}
	return &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_VARIABLE_CHANGE.Enum(),
		Arguments: &apipb.NotificationRequest_VariableMonitorRequest{
			VariableMonitorRequest: monitor,
		},
	}
}
