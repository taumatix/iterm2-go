package iterm2_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// TestUnsubscribeWhileNotificationsArriveDoesNotPanic covers the window between
// the read loop taking its copy of the subscription set and delivering to it.
//
// Conn.dispatch copies the set under the mutex and then releases it before
// delivering, so an Unsubscribe landing in that window closes the channel the
// read loop is about to send on. That is a send on a closed channel, which
// panics — and because it happens on the read loop's goroutine it takes the
// whole program down, not just the subscription.
//
// Unsubscribing from another goroutine while iTerm2 posts notifications is
// ordinary use: it is what any program that stops watching a session does.
func TestUnsubscribeWhileNotificationsArriveDoesNotPanic(t *testing.T) {
	srv := startFake(t, acceptSubscriptions())
	conn := connect(t, srv)

	notifying, stopNotifying := context.WithCancel(context.Background())
	var notifier sync.WaitGroup
	notifier.Add(1)
	go func() {
		defer notifier.Done()
		for notifying.Err() == nil {
			// The error is ignored: the connection closing under it is the normal
			// end of this loop, not a failure.
			_ = srv.Notify(notifying, &apipb.Notification{
				NewSessionNotification: &apipb.NewSessionNotification{
					SessionId: proto.String("s"),
				},
			})
		}
	}()
	t.Cleanup(func() {
		stopNotifying()
		notifier.Wait()
	})

	// Enough churn to land inside the window reliably under -race.
	const rounds = 60
	for range rounds {
		sub, err := conn.SubscribeNewSessions(testContext(t))
		require.NoError(t, err)

		var unsubscribed sync.WaitGroup
		unsubscribed.Add(1)
		go func() {
			defer unsubscribed.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = sub.Unsubscribe(ctx)
		}()
		// Drain concurrently, so the buffer is not simply full for the whole round.
		go func() {
			for range sub.C() {
			}
		}()
		unsubscribed.Wait()
	}

	// The connection must still be usable, which it is not if the read loop died.
	_, err := conn.SubscribeTerminatedSessions(testContext(t))
	assert.NoError(t, err, "the read loop should have survived the churn")
}
