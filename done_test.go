package iterm2_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"
)

func requireClosed(t *testing.T, ch <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(why)
	}
}

// A program that only listens — no request in flight, no subscription — had no
// way to learn iTerm2 had gone. Done is that signal.
func TestDoneClosesWhenITerm2DropsTheConnection(t *testing.T) {
	srv := startFake(t)
	conn := connect(t, srv)
	require.NoError(t, srv.WaitForConnection(5*time.Second))

	select {
	case <-conn.Done():
		t.Fatal("Done closed while the connection was up")
	default:
	}
	assert.NoError(t, conn.Err(), "Err must be nil while the connection is up")

	srv.CloseConnections()

	requireClosed(t, conn.Done(), "Done never closed after iTerm2 dropped the connection")
	err := conn.Err()
	require.Error(t, err)
	assert.ErrorIs(t, err, iterm2.ErrClosed, "ErrClosed is documented as covering iTerm2 quitting")
	assert.NotEqual(t, iterm2.ErrClosed, err,
		"a drop must carry its cause, so it reads differently from the caller's own Close")

	// Requests made after the drop say the same, which is the promise in errors.go.
	_, err = conn.ListSessions(testContext(t))
	assert.True(t, errors.Is(err, iterm2.ErrClosed), "a request after the drop returned %v", err)
}

func TestDoneClosesAndErrIsErrClosedAfterClose(t *testing.T) {
	srv := startFake(t)
	conn := connect(t, srv)

	require.NoError(t, conn.Close())

	requireClosed(t, conn.Done(), "Done never closed after Close")
	assert.ErrorIs(t, conn.Err(), iterm2.ErrClosed)
}
