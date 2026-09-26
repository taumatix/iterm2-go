package iterm2_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"
)

// TestLive drives a real iTerm2 over its real socket.
//
// Everything else in this suite talks to internal/fakeiterm, whose behaviour was
// written from api.proto and iTerm2's source. That proves this package is
// self-consistent, not that it agrees with iTerm2 — so this test exists to check
// the parts a fake cannot: that iTerm2 accepts the handshake headers, that the
// AppleScript cookie exchange works, and that ids taken from one response are
// accepted by the next request.
//
// It is off unless ITERM2_LIVE=1, because it opens and closes a tab in the
// user's own terminal, and because it needs:
//
//   - macOS with iTerm2 running,
//   - the API enabled in Settings > General > Magic > Enable Python API,
//   - permission granted the first time, which raises a prompt.
//
// Run it with:
//
//	ITERM2_LIVE=1 go test -run TestLive -v ./...
func TestLive(t *testing.T) {
	if os.Getenv("ITERM2_LIVE") != "1" {
		t.Skip("set ITERM2_LIVE=1 to run against a real iTerm2 (opens a tab in your terminal)")
	}

	ctx := testContext(t)
	conn, err := iterm2.Connect(ctx, iterm2.WithAdvisoryName("iterm2-go live test"))
	require.NoError(t, err, "Connect; is the Python API enabled in Settings > General > Magic?")
	t.Cleanup(func() { _ = conn.Close() })

	t.Run("ListSessions returns a usable hierarchy", func(t *testing.T) {
		h, err := conn.ListSessions(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, h.Windows, "iTerm2 reported no windows")

		for _, w := range h.Windows {
			assert.NotEmpty(t, w.ID, "a window with no id cannot be addressed")
			for _, tab := range w.Tabs {
				assert.NotEmpty(t, tab.ID)
				assert.Equal(t, w.ID, tab.WindowID)
			}
		}
		assert.NotEmpty(t, h.Sessions(), "iTerm2 reported no sessions")
	})

	t.Run("a created tab is reported by ListSessions and can be closed", func(t *testing.T) {
		// This is the assertion a fake cannot make: that the ids CreateTab answers
		// with are the same ids ListSessions reports, which is what
		// NewTab.TabID's decimal rendering assumes.
		background := false
		created, err := conn.CreateTab(ctx, iterm2.CreateTabOptions{SelectTab: &background})
		require.NoError(t, err)
		require.NotEmpty(t, created.SessionID)

		// Closed even if an assertion below fails, so a failing run does not leave
		// tabs behind.
		t.Cleanup(func() {
			if err := conn.CloseSessions(ctx, true, created.SessionID); err != nil {
				t.Errorf("cleaning up session %s: %v", created.SessionID, err)
			}
		})

		h, err := conn.ListSessions(ctx)
		require.NoError(t, err)

		session := h.Session(created.SessionID)
		require.NotNil(t, session, "the session CreateTab returned is not in the hierarchy")
		assert.Positive(t, session.GridSize.Width, "a live session should have a grid")

		tab := h.Tab(created.TabID)
		assert.NotNil(t, tab,
			"CreateTab's numeric tab_id (%q) does not match any tab id from ListSessions; "+
				"NewTab.TabID's rendering is wrong", created.TabID)
	})

	t.Run("a session reports the variables iTerm2 maintains", func(t *testing.T) {
		h, err := conn.ListSessions(ctx)
		require.NoError(t, err)
		session := h.Sessions()[0]

		// Set by iTerm2 for every session, so a miss here means the variable
		// plumbing is wrong rather than that this session is unusual.
		id, ok, err := session.Variable(ctx, "id")
		require.NoError(t, err)
		assert.True(t, ok, "every session has an id variable")
		assert.NotEmpty(t, id)

		// Names a script sets must begin with "user.".
		require.NoError(t, session.SetVariable(ctx, "user.iterm2GoLiveTest", "hello"))
		got, ok, err := session.Variable(ctx, "user.iterm2GoLiveTest")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "hello", got)
	})

	t.Run("iTerm2 posts a notification for a session it creates", func(t *testing.T) {
		sub, err := conn.SubscribeNewSessions(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = sub.Unsubscribe(ctx) })

		background := false
		created, err := conn.CreateTab(ctx, iterm2.CreateTabOptions{SelectTab: &background})
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.CloseSessions(ctx, true, created.SessionID) })

		got := receive(t, sub)
		assert.Equal(t, created.SessionID, got.GetNewSessionNotification().GetSessionId())
	})
}
