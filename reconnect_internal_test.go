package iterm2

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// These drive the real handshake over a real unix socket against a fake iTerm2
// whose cookie jar behaves like iTerm2's: every cookie is good for one
// handshake (iTermWebSocketCookieJar consumeCookie, pinned 5ed491d), and an
// AppleScript request mints a fresh one. The AppleScript exchange itself is
// faked, because osascript needs a real iTerm2 and Apple Events permission.

// mintingRunner answers iTerm2's two AppleScript requests: "is it running" and
// "give me a cookie". Each cookie it hands out is added to the fake's jar, the
// way iTerm2 adds the cookie it returns.
type mintingRunner struct {
	srv *fakeiterm.Server

	mu     sync.Mutex
	minted int
}

func (m *mintingRunner) run(_ context.Context, script string) (string, error) {
	if strings.Contains(script, "is running") {
		return "yes", nil
	}
	m.mu.Lock()
	m.minted++
	cookie := fmt.Sprintf("applescript-cookie-%d", m.minted)
	m.mu.Unlock()
	m.srv.AddCookie(cookie)
	return cookie + " applescript-key", nil
}

func (m *mintingRunner) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.minted
}

// startJar starts a fake whose only valid cookie, at first, is envCookie.
func startJar(t *testing.T, envCookie string) (*fakeiterm.Server, *mintingRunner) {
	t.Helper()
	srv, err := fakeiterm.Start(fakeiterm.RequireCookie(envCookie), fakeiterm.SingleUseCookies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	forgetSpentEnvCookie(t)
	return srv, &mintingRunner{srv: srv}
}

// forgetSpentEnvCookie isolates tests from each other: which environment cookie
// has been spent is process-wide state, as it is in iTerm2's Python library.
func forgetSpentEnvCookie(t *testing.T) {
	t.Helper()
	spentEnvCookie.forget()
	t.Cleanup(spentEnvCookie.forget)
}

func connectDefault(t *testing.T, srv *fakeiterm.Server, runner scriptRunner) (*Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Connect(ctx,
		WithSocketPath(srv.SocketPath),
		WithCredentialSource(defaultCredentials("test", runner)),
	)
}

func waitDone(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done never closed after iTerm2 dropped the connection")
	}
}

// The case reconnection needs: a process iTerm2 launched holds a cookie in its
// environment, spends it on the first connection, and must reach for a fresh
// one when it connects again. Before this, the second Connect presented the
// spent cookie and got 401 every time, however often it retried.
func TestASecondConnectAsksForAFreshCookieOnceTheEnvironmentsIsSpent(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "env-cookie")
	t.Setenv("ITERM2_KEY", "env-key")
	srv, runner := startJar(t, "env-cookie")

	first, err := connectDefault(t, srv, runner)
	require.NoError(t, err, "the environment's cookie should authorise the first connection")
	assert.Zero(t, runner.count(), "a process holding a cookie must not ask for another")

	srv.CloseConnections()
	waitDone(t, first)

	second, err := connectDefault(t, srv, runner)
	require.NoError(t, err, "the second connection presented a spent cookie")
	defer second.Close()
	assert.Equal(t, 1, runner.count(), "the second connection should have asked iTerm2 for one cookie")

	hs := srv.Handshakes()
	require.Len(t, hs, 2)
	assert.Equal(t, "env-cookie", hs[0].Cookie)
	assert.Equal(t, "applescript-cookie-1", hs[1].Cookie)
}

// A cookie inherited from a parent that already used it is spent before this
// process ever connects. iTerm2's Python library answers the 401 by asking for
// a fresh cookie and trying once more; so does this.
func TestAnInheritedSpentCookieFallsBackToAppleScriptOnce(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "already-spent")
	t.Setenv("ITERM2_KEY", "env-key")
	srv, runner := startJar(t, "some-other-cookie")

	conn, err := connectDefault(t, srv, runner)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, 1, runner.count())
	hs := srv.Handshakes()
	require.Len(t, hs, 2, "one refused attempt with the inherited cookie, one with the fresh one")
	assert.Equal(t, "already-spent", hs[0].Cookie)
	assert.Equal(t, "applescript-cookie-1", hs[1].Cookie)
}

// A fresh cookie that is refused means the API is off or the user said no.
// Asking again would only raise another prompt, so there is no second attempt.
func TestARefusedFreshCookieIsNotRetried(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "")
	t.Setenv("ITERM2_KEY", "")
	srv, err := fakeiterm.Start(fakeiterm.RequireCookie("never-issued"), fakeiterm.SingleUseCookies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	forgetSpentEnvCookie(t)
	runner := &fakeRunner{replies: []string{"yes", "stale-cookie key"}}

	_, err = connectDefault(t, srv, runner)

	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.Len(t, srv.Handshakes(), 1, "a refused AppleScript cookie must not be retried")
}

// Only the default source knows where its cookie came from. A source the
// caller supplied is asked once per Connect, as before.
func TestACallersOwnSourceIsNotRetriedOnUnauthorized(t *testing.T) {
	srv, err := fakeiterm.Start(fakeiterm.RequireCookie("right"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	calls := 0
	src := CredentialSourceFunc(func(context.Context) (Credentials, error) {
		calls++
		return Credentials{Cookie: "wrong", Key: "k"}, nil
	})
	_, err = Connect(context.Background(), WithSocketPath(srv.SocketPath), WithCredentialSource(src))

	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.Equal(t, 1, calls)
}

// If the environment is given a new cookie — a relaunch that re-exported it —
// that one is fresh and should be used, not skipped because an older value
// was spent.
func TestANewEnvironmentCookieIsUsedEvenAfterAnOlderOneWasSpent(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "first")
	t.Setenv("ITERM2_KEY", "k")
	srv, runner := startJar(t, "first")

	c1, err := connectDefault(t, srv, runner)
	require.NoError(t, err)
	_ = c1.Close()

	t.Setenv("ITERM2_COOKIE", "second")
	srv.AddCookie("second")
	c2, err := connectDefault(t, srv, runner)
	require.NoError(t, err)
	defer c2.Close()

	assert.Zero(t, runner.count(), "a fresh environment cookie should not have needed AppleScript")
}
