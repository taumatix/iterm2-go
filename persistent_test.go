package iterm2_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// Persistent is the loop the README asked every program to write — reconnect,
// re-subscribe, resynchronise — done once. These drive it over a real unix
// socket against the fake, dropping the connection mid-stream the way an
// iTerm2 restart does.

// countingSubscriptions answers NotificationRequests with OK and counts the
// subscribes, so a test can see a re-subscription reach the new connection.
func countingSubscriptions(subscribes *atomic.Int32) fakeiterm.Option {
	return fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		if nr := req.GetNotificationRequest(); nr != nil && nr.GetSubscribe() {
			subscribes.Add(1)
		}
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
				NotificationResponse: &apipb.NotificationResponse{Status: apipb.NotificationResponse_OK.Enum()},
			},
		}
	})
}

func connectPersistent(t *testing.T, srv *fakeiterm.Server) *iterm2.Persistent {
	t.Helper()
	p, err := iterm2.ConnectPersistent(testContext(t),
		iterm2.WithSocketPath(srv.SocketPath),
		iterm2.WithCredentials(testCookie, "key-xyz"),
		iterm2.WithReconnectBackoff(10*time.Millisecond, 100*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func newSession(id string) *apipb.Notification {
	return &apipb.Notification{NewSessionNotification: &apipb.NewSessionNotification{SessionId: proto.String(id)}}
}

func nextReconnect(t *testing.T, p *iterm2.Persistent) iterm2.Reconnect {
	t.Helper()
	select {
	case r := <-p.Reconnects():
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("no reconnect was reported")
		return iterm2.Reconnect{}
	}
}

func receiveDurable(t *testing.T, sub *iterm2.DurableSubscription) *apipb.Notification {
	t.Helper()
	select {
	case n, ok := <-sub.C():
		require.True(t, ok, "the durable subscription's channel closed: %v", sub.Err())
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
		return nil
	}
}

func TestADurableSubscriptionKeepsDeliveringAcrossAReconnect(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, countingSubscriptions(&subscribes))
	p := connectPersistent(t, srv)

	sub, err := p.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)
	require.NoError(t, srv.Notify(testContext(t), newSession("before")))
	assert.Equal(t, "before", receiveDurable(t, sub).GetNewSessionNotification().GetSessionId())

	srv.CloseConnections()
	r := nextReconnect(t, p)

	assert.Equal(t, 1, r.Count)
	assert.ErrorIs(t, r.Cause, iterm2.ErrClosed, "the reconnect should say why the old connection ended")
	assert.Equal(t, int32(2), subscribes.Load(), "the subscription was not re-made on the new connection")

	require.NoError(t, srv.Notify(testContext(t), newSession("after")))
	assert.Equal(t, "after", receiveDurable(t, sub).GetNewSessionNotification().GetSessionId(),
		"the same channel must carry notifications from the new connection")
}

// The reconnect is announced only once its subscriptions are back, so a caller
// that resynchronises on it cannot miss a change made between the two.
func TestAReconnectIsReportedAfterItsSubscriptionsAreRestored(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, countingSubscriptions(&subscribes))
	p := connectPersistent(t, srv)
	_, err := p.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)
	_, err = p.SubscribeTerminatedSessions(testContext(t))
	require.NoError(t, err)

	srv.CloseConnections()
	nextReconnect(t, p)

	assert.Equal(t, int32(4), subscribes.Load(), "both subscriptions must be back before the reconnect is announced")
}

// A caller that falls behind still learns how many reconnects it missed: the
// channel holds the latest, and Count keeps rising.
func TestReconnectsCoalesceButCountEveryOne(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, countingSubscriptions(&subscribes))
	p := connectPersistent(t, srv)

	for i := 1; i <= 3; i++ {
		srv.CloseConnections()
		require.Eventually(t, func() bool { return srv.ConnectionCount() == 1 }, 10*time.Second, 5*time.Millisecond,
			"reconnect %d did not happen", i)
		time.Sleep(50 * time.Millisecond)
	}

	assert.Equal(t, 3, nextReconnect(t, p).Count)
}

func TestUnsubscribeIsNotUndoneByAReconnect(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, countingSubscriptions(&subscribes))
	p := connectPersistent(t, srv)
	sub, err := p.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, sub.Unsubscribe(testContext(t)))
	_, open := <-sub.C()
	assert.False(t, open, "Unsubscribe must close the channel")

	srv.CloseConnections()
	nextReconnect(t, p)
	assert.Equal(t, int32(1), subscribes.Load(), "an unsubscribed subscription came back after the reconnect")
}

func TestWhileReconnectingCallsSaySo(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	p := connectPersistent(t, srv)

	// Gone for good: nothing to reconnect to.
	require.NoError(t, srv.Close())

	require.Eventually(t, func() bool {
		_, err := p.Conn()
		return errors.Is(err, iterm2.ErrReconnecting)
	}, 10*time.Second, 10*time.Millisecond)

	_, err := p.Do(testContext(t), &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ListSessionsRequest{ListSessionsRequest: &apipb.ListSessionsRequest{}},
	})
	assert.ErrorIs(t, err, iterm2.ErrReconnecting)
	assert.ErrorIs(t, err, iterm2.ErrClosed, "ErrReconnecting is a kind of ErrClosed, so existing checks keep working")
}

func TestClosingAPersistentConnectionEndsItsSubscriptions(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	p := connectPersistent(t, srv)
	sub, err := p.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, p.Close())

	select {
	case _, open := <-sub.C():
		assert.False(t, open)
	case <-time.After(5 * time.Second):
		t.Fatal("Close left a durable subscription open")
	}
	_, err = p.Conn()
	assert.ErrorIs(t, err, iterm2.ErrClosed)
}
