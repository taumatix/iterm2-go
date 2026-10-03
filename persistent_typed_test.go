package iterm2_test

import (
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

// A program that holds a Persistent rather than a *Conn should be able to make
// every typed call through it, so there is no *Conn for it to keep past a
// reconnect. These drive those calls across one, over the fake's real socket.

// answerTypedCalls answers the requests these tests make, counting the
// subscribes so a test can see them re-made on a new connection.
func answerTypedCalls(subscribes *atomic.Int32) fakeiterm.Option {
	return fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		switch {
		case req.GetListSessionsRequest() != nil:
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_ListSessionsResponse{
				ListSessionsResponse: &apipb.ListSessionsResponse{Windows: []*apipb.ListSessionsResponse_Window{{
					WindowId: proto.String("w-1"),
					Tabs: []*apipb.ListSessionsResponse_Tab{{
						TabId: proto.String("t-1"),
						Root: &apipb.SplitTreeNode{Links: []*apipb.SplitTreeNode_SplitTreeLink{
							sessionLink("s-1", "one"),
						}},
					}},
				}}},
			}}
		case req.GetSendTextRequest() != nil:
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_SendTextResponse{
				SendTextResponse: &apipb.SendTextResponse{Status: apipb.SendTextResponse_OK.Enum()},
			}}
		case req.GetVariableRequest() != nil:
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_VariableResponse{
				VariableResponse: &apipb.VariableResponse{Status: apipb.VariableResponse_OK.Enum()},
			}}
		default:
			if nr := req.GetNotificationRequest(); nr != nil && nr.GetSubscribe() {
				subscribes.Add(1)
			}
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
				NotificationResponse: &apipb.NotificationResponse{Status: apipb.NotificationResponse_OK.Enum()},
			}}
		}
	})
}

// Both are a Client, so code written against one works with the other.
var (
	_ iterm2.Client = (*iterm2.Conn)(nil)
	_ iterm2.Client = (*iterm2.Persistent)(nil)
)

// useClient makes the calls a typical program makes, through the interface.
func useClient(t *testing.T, c iterm2.Client) {
	t.Helper()
	h, err := c.ListSessions(testContext(t))
	require.NoError(t, err)
	require.NotNil(t, h.Session("s-1"), "the hierarchy did not come through")
	require.NoError(t, c.SendText(testContext(t), "s-1", "ls\n", false))
	require.NoError(t, c.SetStringVariable(testContext(t), iterm2.ScopeSession, "s-1", "user.tag", "x"))
}

func TestTypedCallsOnAPersistentReachTheConnectionThatReplacedTheFirst(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, answerTypedCalls(&subscribes))
	p := connectPersistent(t, srv)
	useClient(t, p)

	srv.CloseConnections()
	nextReconnect(t, p)

	useClient(t, p)
}

func TestAConnIsAClientToo(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, answerTypedCalls(&subscribes))
	useClient(t, connect(t, srv))
}

func TestTypedCallsOnAPersistentSayWhenITerm2IsAway(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, answerTypedCalls(&subscribes))
	p := connectPersistent(t, srv)
	require.NoError(t, srv.Close())
	require.Eventually(t, func() bool {
		_, err := p.Conn()
		return err != nil
	}, 10*time.Second, 10*time.Millisecond)

	_, err := p.ListSessions(testContext(t))
	assert.ErrorIs(t, err, iterm2.ErrReconnecting)
	assert.ErrorIs(t, p.SendText(testContext(t), "s-1", "ls\n", false), iterm2.ErrReconnecting)
	assert.ErrorIs(t, p.Activate(testContext(t), iterm2.ActivateOptions{ActivateApp: true}), iterm2.ErrReconnecting)
}

func TestEverySubscribeHelperOnAPersistentIsRemadeOnReconnect(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, answerTypedCalls(&subscribes))
	p := connectPersistent(t, srv)

	ctx := testContext(t)
	_, err := p.SubscribeLayoutChanges(ctx)
	require.NoError(t, err)
	_, err = p.SubscribeFocusChanges(ctx)
	require.NoError(t, err)
	vars, err := p.SubscribeVariableChanges(ctx, iterm2.ScopeSession, "s-1", "jobName")
	require.NoError(t, err)
	require.Equal(t, int32(3), subscribes.Load())

	srv.CloseConnections()
	nextReconnect(t, p)
	assert.Equal(t, int32(6), subscribes.Load(), "every subscription must be re-made on the new connection")

	n := &apipb.Notification{VariableChangedNotification: &apipb.VariableChangedNotification{
		Name: proto.String("jobName"), JsonNewValue: proto.String(`"vim"`),
	}}
	require.NoError(t, srv.Notify(testContext(t), n))
	assert.Equal(t, `"vim"`, receiveDurable(t, vars).GetVariableChangedNotification().GetJsonNewValue())
}

// A Session, Tab or Window carries the means to act on itself. Listed through a
// Persistent, it must act through the Persistent, not the connection that
// happened to list it, or it is dead after the first restart.
func TestASessionListedBeforeAReconnectStillWorksAfterIt(t *testing.T) {
	var subscribes atomic.Int32
	srv := startFake(t, answerTypedCalls(&subscribes))
	p := connectPersistent(t, srv)
	h, err := p.ListSessions(testContext(t))
	require.NoError(t, err)
	s := h.Session("s-1")
	require.NotNil(t, s)

	srv.CloseConnections()
	nextReconnect(t, p)

	assert.NoError(t, s.SendText(testContext(t), "ls\n"))
	assert.NoError(t, s.SetVariable(testContext(t), "user.tag", "x"))
}
