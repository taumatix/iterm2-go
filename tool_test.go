package iterm2_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// toolRegistry plays iTerm2's side of RegisterToolRequest: it records each
// request and answers with the next status, OK once they run out.
type toolRegistry struct {
	mu       sync.Mutex
	requests []*apipb.RegisterToolRequest
	statuses []apipb.RegisterToolResponse_Status
}

func (r *toolRegistry) option() fakeiterm.Option {
	return fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		if rt := req.GetRegisterToolRequest(); rt != nil {
			r.mu.Lock()
			r.requests = append(r.requests, rt)
			status := apipb.RegisterToolResponse_OK
			if len(r.statuses) > 0 {
				status, r.statuses = r.statuses[0], r.statuses[1:]
			}
			r.mu.Unlock()
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_RegisterToolResponse{
				RegisterToolResponse: &apipb.RegisterToolResponse{Status: status.Enum()},
			}}
		}
		return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{Status: apipb.NotificationResponse_OK.Enum()},
		}}
	})
}

func (r *toolRegistry) seen() []*apipb.RegisterToolRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*apipb.RegisterToolRequest(nil), r.requests...)
}

var panel = iterm2.Tool{
	Name:       "Sessions",
	Identifier: "com.example.sessions",
	URL:        "http://127.0.0.1:8080/?token=x",
}

func TestRegisterToolSendsWhatITerm2Needs(t *testing.T) {
	reg := &toolRegistry{}
	conn := connect(t, startFake(t, reg.option()))

	tool := panel
	tool.Reveal = true
	require.NoError(t, conn.RegisterTool(testContext(t), tool))

	got := reg.seen()
	require.Len(t, got, 1)
	assert.Equal(t, "Sessions", got[0].GetName())
	assert.Equal(t, "com.example.sessions", got[0].GetIdentifier())
	assert.Equal(t, "http://127.0.0.1:8080/?token=x", got[0].GetURL())
	assert.Equal(t, apipb.RegisterToolRequest_WEB_VIEW_TOOL, got[0].GetToolType())
	assert.True(t, got[0].GetRevealIfAlreadyRegistered())
}

func TestRegisterToolReportsARefusal(t *testing.T) {
	reg := &toolRegistry{statuses: []apipb.RegisterToolResponse_Status{apipb.RegisterToolResponse_PERMISSION_DENIED}}
	conn := connect(t, startFake(t, reg.option()))

	err := conn.RegisterTool(testContext(t), panel)
	var status *iterm2.StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, "PERMISSION_DENIED", status.Status)
}

// iTerm2 answers a tool with no name, identifier or URL with REQUEST_MALFORMED
// after the fact; refusing first says which field is missing.
func TestRegisterToolRefusesAnIncompleteToolWithoutAsking(t *testing.T) {
	reg := &toolRegistry{}
	conn := connect(t, startFake(t, reg.option()))

	for _, tool := range []iterm2.Tool{
		{Identifier: "a", URL: "http://x"},
		{Name: "a", URL: "http://x"},
		{Name: "a", Identifier: "a"},
	} {
		err := conn.RegisterTool(testContext(t), tool)
		var api *iterm2.APIError
		assert.ErrorAs(t, err, &api, "%+v", tool)
	}
	assert.Empty(t, reg.seen(), "an incomplete tool was sent to iTerm2")
}

// iTerm2 forgets a toolbelt tool when it quits. A Persistent registers it again
// on every new connection, before announcing the reconnect, and without
// revealing it: a restart should not pop the toolbelt open.
func TestAPersistentRegistersItsToolsAgainOnEveryConnection(t *testing.T) {
	reg := &toolRegistry{}
	srv := startFake(t, reg.option())
	p := connectPersistent(t, srv)

	tool := panel
	tool.Reveal = true
	require.NoError(t, p.RegisterTool(testContext(t), tool))

	srv.CloseConnections()
	r := nextReconnect(t, p)
	assert.NoError(t, r.ToolErr)

	got := reg.seen()
	require.Len(t, got, 2, "the tool was not registered again on the new connection")
	assert.True(t, got[0].GetRevealIfAlreadyRegistered())
	assert.False(t, got[1].GetRevealIfAlreadyRegistered(), "a reconnect revealed the tool")
	assert.Equal(t, got[0].GetURL(), got[1].GetURL())
}

// Registering under the same identifier again replaces the tool, here and in
// iTerm2, so a new URL is the one restored after a reconnect.
func TestAPersistentRestoresTheLatestRegistrationOfATool(t *testing.T) {
	reg := &toolRegistry{}
	srv := startFake(t, reg.option())
	p := connectPersistent(t, srv)

	require.NoError(t, p.RegisterTool(testContext(t), panel))
	moved := panel
	moved.URL = "http://127.0.0.1:9090/"
	require.NoError(t, p.RegisterTool(testContext(t), moved))

	srv.CloseConnections()
	nextReconnect(t, p)

	got := reg.seen()
	require.Len(t, got, 3, "one tool, registered twice, should be restored once")
	assert.Equal(t, "http://127.0.0.1:9090/", got[2].GetURL())
}

// A tool iTerm2 refuses on the new connection is reported on the Reconnect,
// since a Persistent has no other way to say so.
func TestAToolRefusedOnReconnectIsReported(t *testing.T) {
	reg := &toolRegistry{}
	srv := startFake(t, reg.option())
	p := connectPersistent(t, srv)
	require.NoError(t, p.RegisterTool(testContext(t), panel))

	reg.mu.Lock()
	reg.statuses = []apipb.RegisterToolResponse_Status{apipb.RegisterToolResponse_PERMISSION_DENIED}
	reg.mu.Unlock()
	srv.CloseConnections()
	r := nextReconnect(t, p)

	var status *iterm2.StatusError
	require.True(t, errors.As(r.ToolErr, &status), "got %v", r.ToolErr)
	assert.Equal(t, "PERMISSION_DENIED", status.Status)
}

// A tool registered while iTerm2 is away is refused, like any call, and is
// not remembered: the caller decides whether to try again.
func TestRegisterToolWhileAwaySaysSo(t *testing.T) {
	reg := &toolRegistry{}
	srv := startFake(t, reg.option())
	p := connectPersistent(t, srv)
	require.NoError(t, srv.Close())
	require.Eventually(t, func() bool { _, err := p.Conn(); return err != nil }, tenSecondsForTool, tenMillisForTool)

	assert.ErrorIs(t, p.RegisterTool(testContext(t), panel), iterm2.ErrReconnecting)
}

const (
	tenSecondsForTool = 10_000_000_000
	tenMillisForTool  = 10_000_000
)
