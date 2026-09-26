package iterm2_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// acceptSubscriptions answers every NotificationRequest with OK, which is what
// iTerm2 does for a subscription it accepts.
func acceptSubscriptions() fakeiterm.Option {
	return fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
				NotificationResponse: &apipb.NotificationResponse{
					Status: apipb.NotificationResponse_OK.Enum(),
				},
			},
		}
	})
}

// receive waits for one notification, failing the test if none arrives.
func receive(t *testing.T, sub *iterm2.Subscription) *apipb.Notification {
	t.Helper()
	select {
	case n, ok := <-sub.C():
		require.True(t, ok, "the subscription channel closed")
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
		return nil
	}
}

func TestSubscribeDeliversTheNotificationITerm2Posts(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	sub, err := conn.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		NewSessionNotification: &apipb.NewSessionNotification{
			SessionId: proto.String("s-new"),
		},
	}))

	got := receive(t, sub)
	assert.Equal(t, "s-new", got.GetNewSessionNotification().GetSessionId())
}

func TestSubscribeAsksForTheTypeItWasNamed(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{
				Status: apipb.NotificationResponse_OK.Enum(),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.SubscribeLayoutChanges(testContext(t))
	require.NoError(t, err)

	req := rec.last(t).GetNotificationRequest()
	require.NotNil(t, req)
	assert.Equal(t, apipb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE, req.GetNotificationType())
	// Subscribe sets this itself; a caller must not have to.
	assert.True(t, req.GetSubscribe())
}

func TestTwoSubscriptionsOnOneConnectionDoNotSeeEachOthersTraffic(t *testing.T) {
	// iTerm2 posts everything down one socket with nothing tying a notification
	// back to the request that asked for it, so the filtering is this package's
	// job.
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	newSessions, err := conn.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)
	terminated, err := conn.SubscribeTerminatedSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		TerminateSessionNotification: &apipb.TerminateSessionNotification{
			SessionId: proto.String("s-gone"),
		},
	}))

	got := receive(t, terminated)
	assert.Equal(t, "s-gone", got.GetTerminateSessionNotification().GetSessionId())

	select {
	case n := <-newSessions.C():
		t.Fatalf("the new-session subscription received %v", n)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPerSessionSubscriptionIgnoresOtherSessions(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	sub, err := conn.Subscribe(testContext(t), &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_KEYSTROKE.Enum(),
		Session:          proto.String("s-mine"),
	})
	require.NoError(t, err)

	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		KeystrokeNotification: &apipb.KeystrokeNotification{Session: proto.String("s-theirs")},
	}))
	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		KeystrokeNotification: &apipb.KeystrokeNotification{
			Session:    proto.String("s-mine"),
			Characters: proto.String("x"),
		},
	}))

	got := receive(t, sub)
	assert.Equal(t, "s-mine", got.GetKeystrokeNotification().GetSession())
	assert.Equal(t, "x", got.GetKeystrokeNotification().GetCharacters())
}

func TestSubscriptionForAllSessionsSeesEveryone(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	sub, err := conn.Subscribe(testContext(t), &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_KEYSTROKE.Enum(),
		Session:          proto.String(iterm2.SessionAll),
	})
	require.NoError(t, err)

	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		KeystrokeNotification: &apipb.KeystrokeNotification{Session: proto.String("s-anyone")},
	}))

	got := receive(t, sub)
	assert.Equal(t, "s-anyone", got.GetKeystrokeNotification().GetSession())
}

func TestSubscribeRejectsARequestWithNoType(t *testing.T) {
	conn := connect(t, startFake(t, acceptSubscriptions()))

	_, err := conn.Subscribe(testContext(t), &apipb.NotificationRequest{})
	assert.Error(t, err)

	_, err = conn.Subscribe(testContext(t), nil)
	assert.Error(t, err)
}

func TestSubscribeRejectsKeystrokeFilterBecauseItPostsNothing(t *testing.T) {
	// api.proto: "Does not send a notification". A channel that can never produce
	// anything would be a trap.
	conn := connect(t, startFake(t, acceptSubscriptions()))

	_, err := conn.Subscribe(testContext(t), &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_KEYSTROKE_FILTER.Enum(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEYSTROKE_FILTER")
}

func TestSubscribeSurfacesAlreadySubscribed(t *testing.T) {
	srv := startFake(t, fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
				NotificationResponse: &apipb.NotificationResponse{
					Status: apipb.NotificationResponse_ALREADY_SUBSCRIBED.Enum(),
				},
			},
		}
	}))
	conn := connect(t, srv)

	_, err := conn.SubscribeNewSessions(testContext(t))

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "ALREADY_SUBSCRIBED", statusErr.Status)
}

func TestARefusedSubscriptionIsNotLeftRegistered(t *testing.T) {
	// A subscription registered before the request goes out must be taken back off
	// when iTerm2 refuses it, or it would keep receiving notifications.
	srv := startFake(t, fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
				NotificationResponse: &apipb.NotificationResponse{
					Status: apipb.NotificationResponse_ALREADY_SUBSCRIBED.Enum(),
				},
			},
		}
	}))
	conn := connect(t, srv)

	_, err := conn.SubscribeNewSessions(testContext(t))
	require.Error(t, err)

	// Posting a notification must not panic on a closed channel, which is what a
	// still-registered subscription would do.
	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		NewSessionNotification: &apipb.NewSessionNotification{SessionId: proto.String("s")},
	}))
	time.Sleep(100 * time.Millisecond)
}

func TestUnsubscribeClosesTheChannelAndTellsITerm2(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{
				Status: apipb.NotificationResponse_OK.Enum(),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	sub, err := conn.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)
	require.NoError(t, sub.Unsubscribe(testContext(t)))

	select {
	case _, ok := <-sub.C():
		assert.False(t, ok, "the channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("the channel was not closed")
	}

	// Unsubscribing must name the same type it subscribed with.
	req := rec.last(t).GetNotificationRequest()
	require.NotNil(t, req)
	assert.False(t, req.GetSubscribe())
	assert.Equal(t, apipb.NotificationType_NOTIFY_ON_NEW_SESSION, req.GetNotificationType())

	require.NoError(t, sub.Unsubscribe(testContext(t)), "unsubscribing twice is harmless")
}

func TestSubscriptionChannelClosesWhenTheConnectionGoesAway(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	sub, err := conn.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)

	srv.CloseConnections()

	select {
	case _, ok := <-sub.C():
		assert.False(t, ok, "the channel should be closed, not delivering")
	case <-time.After(5 * time.Second):
		t.Fatal("the channel stayed open after the connection went away")
	}
}

func TestSlowConsumerDropsNotificationsRatherThanStalling(t *testing.T) {
	// The read loop delivers notifications, so blocking on a full buffer would
	// stall every other request on the connection.
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv, iterm2.WithSubscriptionBuffer(1))

	sub, err := conn.SubscribeNewSessions(testContext(t))
	require.NoError(t, err)

	for range 20 {
		require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
			NewSessionNotification: &apipb.NewSessionNotification{SessionId: proto.String("s")},
		}))
	}

	// The connection must still answer while the subscription buffer is full.
	deadline := time.Now().Add(5 * time.Second)
	for sub.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.Positive(t, sub.Dropped(), "notifications should be counted as dropped")

	_, err = conn.SubscribeTerminatedSessions(testContext(t))
	assert.NoError(t, err, "the connection should still work with a full subscription buffer")
}

func TestSubscribeVariableChangesSendsTheMonitorRequest(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{
				Status: apipb.NotificationResponse_OK.Enum(),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.SubscribeVariableChanges(testContext(t), iterm2.ScopeSession, "s-1", "jobName")
	require.NoError(t, err)

	req := rec.last(t).GetNotificationRequest()
	require.NotNil(t, req)
	assert.Equal(t, apipb.NotificationType_NOTIFY_ON_VARIABLE_CHANGE, req.GetNotificationType())
	monitor := req.GetVariableMonitorRequest()
	require.NotNil(t, monitor)
	assert.Equal(t, "jobName", monitor.GetName())
	assert.Equal(t, apipb.VariableScope_SESSION, monitor.GetScope())
	assert.Equal(t, "s-1", monitor.GetIdentifier())
}

func TestVariableChangeNotificationsAreNotFilteredBySession(t *testing.T) {
	// The VariableMonitorRequest scopes these, not the session field, so a
	// per-session filter would drop every one of them.
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	sub, err := conn.SubscribeVariableChanges(testContext(t), iterm2.ScopeSession, "s-1", "jobName")
	require.NoError(t, err)

	require.NoError(t, srv.Notify(testContext(t), &apipb.Notification{
		VariableChangedNotification: &apipb.VariableChangedNotification{
			Name:         proto.String("jobName"),
			JsonNewValue: proto.String(`"vim"`),
		},
	}))

	got := receive(t, sub)
	assert.Equal(t, "jobName", got.GetVariableChangedNotification().GetName())
}
