package iterm2_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

const testCookie = "cookie-abc"

// startFake brings up a fake iTerm2 on a real unix socket.
func startFake(t *testing.T, opts ...fakeiterm.Option) *fakeiterm.Server {
	t.Helper()
	opts = append([]fakeiterm.Option{fakeiterm.RequireCookie(testCookie)}, opts...)
	srv, err := fakeiterm.Start(opts...)
	require.NoError(t, err, "starting fake iTerm2")
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// connect dials the fake over its unix socket, exercising the real handshake.
func connect(t *testing.T, srv *fakeiterm.Server, opts ...iterm2.Option) *iterm2.Conn {
	t.Helper()
	opts = append([]iterm2.Option{
		iterm2.WithSocketPath(srv.SocketPath),
		iterm2.WithCredentials(testCookie, "key-xyz"),
	}, opts...)

	conn, err := iterm2.Connect(testContext(t), opts...)
	require.NoError(t, err, "Connect")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// testContext bounds every call so a hang fails the test instead of the suite.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestConnectSendsTheHeadersITerm2Validates(t *testing.T) {
	srv := startFake(t)
	connect(t, srv, iterm2.WithAdvisoryName("my-program"))

	hs := srv.Handshakes()
	require.Len(t, hs, 1)
	got := hs[0]

	assert.Equal(t, testCookie, got.Cookie)
	assert.Equal(t, "key-xyz", got.Key)
	// iTerm2 splits this on space and compares the second field to a per-language
	// minimum, so a single-field value would be rejected outright.
	assert.Equal(t, "go "+iterm2.LibraryVersion, got.LibraryVersion)
	assert.Equal(t, "my-program", got.AdvisoryName)
	// iTerm2 requires an origin whose host is localhost.
	assert.Equal(t, "ws://localhost/", got.Origin)
	// Without this iTerm2 may raise a modal a library cannot answer.
	assert.Equal(t, "true", got.DisableAuthUI)
	assert.Equal(t, []string{fakeiterm.Subprotocol}, got.Subprotocols)
}

func TestConnectWithoutAnAdvisoryNameStillNamesSomething(t *testing.T) {
	srv := startFake(t)
	connect(t, srv)

	assert.NotEmpty(t, srv.Handshakes()[0].AdvisoryName,
		"iTerm2's script console would show a blank row")
}

func TestConnectRefusedIsErrUnauthorized(t *testing.T) {
	srv := startFake(t)

	_, err := iterm2.Connect(testContext(t),
		iterm2.WithSocketPath(srv.SocketPath),
		iterm2.WithCredentials("wrong-cookie", "key"),
	)
	assert.ErrorIs(t, err, iterm2.ErrUnauthorized)
}

func TestConnectFallsBackToTCPWhenNoSocketExists(t *testing.T) {
	srv := startFake(t)

	// Nothing listens on port 1, so the fallback is observable as a dial failure
	// naming it rather than the socket that does exist.
	_, err := iterm2.Connect(testContext(t),
		iterm2.WithSocketPath(srv.SocketPath+"-absent"),
		iterm2.WithTCPAddress("127.0.0.1:1"),
		iterm2.WithCredentials(testCookie, "key"),
	)
	require.Error(t, err, "want a failure against the TCP fallback")
	assert.Contains(t, err.Error(), "127.0.0.1:1",
		"the error should name the address it fell back to")
}

func TestDoMatchesConcurrentResponsesToTheirRequests(t *testing.T) {
	// Each response echoes its own request's text, so a mismatched id shows up as
	// the wrong text rather than as a hang.
	srv := startFake(t, fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		text := req.GetSendTextRequest().GetText()
		// A staggered delay reorders the replies relative to the requests.
		time.Sleep(time.Duration(len(text)%5) * 10 * time.Millisecond)
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_VariableResponse{
				VariableResponse: &apipb.VariableResponse{Values: []string{text}},
			},
		}
	}))
	conn := connect(t, srv)
	ctx := testContext(t)

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("request-%d", i)
			resp, err := conn.Do(ctx, &apipb.ClientOriginatedMessage{
				Submessage: &apipb.ClientOriginatedMessage_SendTextRequest{
					SendTextRequest: &apipb.SendTextRequest{Text: proto.String(want)},
				},
			})
			if err != nil {
				errs <- err
				return
			}
			if got := resp.GetVariableResponse().GetValues(); len(got) != 1 || got[0] != want {
				errs <- fmt.Errorf("got %v, want [%s]", got, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestDoDoesNotMutateTheCallersRequest(t *testing.T) {
	srv := startFake(t)
	conn := connect(t, srv)

	req := listSessions()
	_, err := conn.Do(testContext(t), req)
	require.NoError(t, err)

	// Reusing a request is a natural thing to do, and would break if Do stamped
	// the id onto the caller's copy.
	assert.Nil(t, req.Id, "Do set an id on the caller's request")
}

func TestDoReportsTheErrorFieldAsAPIError(t *testing.T) {
	srv := startFake(t, fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		// api.proto puts `error` inside the submessage oneof, so a response
		// carries either an error or a body, never both.
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_Error{Error: "could not parse that"},
		}
	}))
	conn := connect(t, srv)

	_, err := conn.Do(testContext(t), listSessions())

	var apiErr *iterm2.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "could not parse that", apiErr.Message)
}

func TestDoRespectsContextCancellationAndLeavesTheConnectionUsable(t *testing.T) {
	// The first request goes unanswered; later ones are answered, so the test can
	// show the connection survived the abandoned one.
	var mu sync.Mutex
	var seen int
	srv := startFake(t, fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		mu.Lock()
		seen++
		first := seen == 1
		mu.Unlock()
		if first {
			return nil
		}
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_ListSessionsResponse{
				ListSessionsResponse: &apipb.ListSessionsResponse{},
			},
		}
	}))
	conn := connect(t, srv)

	abandoned, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := conn.Do(abandoned, listSessions())
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = conn.ListSessions(testContext(t))
	assert.NoError(t, err, "the connection should survive an abandoned request")
}

func TestRequestsFailWithErrClosedAfterClose(t *testing.T) {
	srv := startFake(t)
	conn := connect(t, srv)

	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close(), "closing twice is harmless")

	_, err := conn.ListSessions(testContext(t))
	assert.ErrorIs(t, err, iterm2.ErrClosed)
}

func TestPendingRequestFailsWhenITerm2Quits(t *testing.T) {
	// The handler never answers, so the request is still outstanding when the
	// connection is dropped from the other end.
	srv := startFake(t, fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		return nil
	}))
	conn := connect(t, srv)

	done := make(chan error, 1)
	go func() {
		_, err := conn.ListSessions(context.Background())
		done <- err
	}()

	require.NoError(t, srv.WaitForConnection(5*time.Second))
	// Give the request time to reach the server before pulling the socket away.
	time.Sleep(100 * time.Millisecond)
	srv.CloseConnections()

	select {
	case err := <-done:
		assert.Error(t, err, "the request should fail once iTerm2 has gone")
	case <-time.After(10 * time.Second):
		t.Fatal("the request hung after iTerm2 went away")
	}
}

func TestATextFrameEndsTheConnection(t *testing.T) {
	// iTerm2 only ever sends binary. Anything else means this is not iTerm2, and
	// guessing at the payload would be worse than failing.
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{}))
	conn := connect(t, srv)

	// The connection is healthy first, so the failure below is caused by the text
	// frame rather than by a request the fake does not answer.
	_, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, srv.WaitForConnection(5*time.Second))
	require.NoError(t, srv.SendRaw(testContext(t), websocket.MessageText, []byte("hello")))

	assert.Error(t, waitForBrokenConnection(conn), "the connection survived a text frame")
}

func TestAMalformedMessageEndsTheConnection(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{}))
	conn := connect(t, srv)

	_, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	require.NoError(t, srv.WaitForConnection(5*time.Second))
	// Field 1 declared as a varint and then truncated: not a decodable protobuf.
	require.NoError(t, srv.SendRaw(testContext(t), websocket.MessageBinary, []byte{0x08}))

	assert.Error(t, waitForBrokenConnection(conn), "the connection survived an undecodable message")
}

func TestDoRejectsANilRequest(t *testing.T) {
	srv := startFake(t)
	conn := connect(t, srv)

	_, err := conn.Do(testContext(t), nil)
	assert.Error(t, err)
}

// waitForBrokenConnection polls until a request fails for a reason other than
// its own deadline, and returns that error. It returns nil when the connection
// stays usable, which is a failure at every call site.
//
// The deadline is classified before cancel() runs: afterwards ctx.Err() is
// always non-nil, so testing it there would make every attempt look like a
// timeout and the loop could never report anything.
func waitForBrokenConnection(conn *iterm2.Conn) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := conn.ListSessions(ctx)
		timedOut := errors.Is(err, context.DeadlineExceeded)
		cancel()
		if err != nil && !timedOut {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// answerListSessions replies to ListSessions with hierarchy, so that a healthy
// connection returns no error and [waitForBrokenConnection] is measuring the
// connection rather than an unhandled request.
func answerListSessions(hierarchy *apipb.ListSessionsResponse) fakeiterm.Option {
	return fakeiterm.WithHandler(func(*apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_ListSessionsResponse{
				ListSessionsResponse: hierarchy,
			},
		}
	})
}

func listSessions() *apipb.ClientOriginatedMessage {
	return &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ListSessionsRequest{
			ListSessionsRequest: &apipb.ListSessionsRequest{},
		},
	}
}
