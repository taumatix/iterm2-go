package iterm2_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

var copyPath = iterm2.ContextMenuItem{
	RPC: iterm2.RPC{
		Name:      "copy_path",
		Arguments: []string{"path"},
		Defaults:  map[string]string{"path": "session.path"},
		Handler: func(_ context.Context, args map[string]json.RawMessage) (any, error) {
			return nil, nil
		},
	},
	DisplayName: "Copy working directory",
	Identifier:  "com.example.copy-path",
}

// A context-menu item is an RPC registered in the CONTEXT_MENU role, with the
// label iTerm2 shows in a session's context menu.
func TestRegisterContextMenuItemSendsTheRoleAndItsAttributes(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))

	reg, err := conn.RegisterContextMenuItem(testContext(t), copyPath)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	got := host.registered()
	require.Len(t, got, 1)
	assert.Equal(t, "copy_path", got[0].GetName())
	assert.Equal(t, apipb.RPCRegistrationRequest_CONTEXT_MENU, got[0].GetRole())
	attrs := got[0].GetContextMenuAttributes()
	require.NotNil(t, attrs)
	assert.Equal(t, "Copy working directory", attrs.GetDisplayName())
	assert.Equal(t, "com.example.copy-path", attrs.GetUniqueIdentifier())
	assert.Nil(t, got[0].GetSessionTitleAttributes())
}

// Choosing the item calls the function, with the variables it asked for.
func TestChoosingAContextMenuItemCallsItsFunction(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	called := make(chan string, 1)
	item := copyPath
	item.Handler = func(_ context.Context, args map[string]json.RawMessage) (any, error) {
		var path string
		_ = json.Unmarshal(args["path"], &path)
		called <- path
		return nil, nil
	}
	reg, err := conn.RegisterContextMenuItem(testContext(t), item)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	require.NoError(t, srv.Notify(testContext(t), call("m-1", "copy_path", map[string]string{"path": `"/srv/app"`})))
	assert.Equal(t, "/srv/app", <-called)
	assert.Equal(t, "m-1", host.result(t).GetRequestId(), "the call must still be answered")
}

func TestRegisterContextMenuItemRefusesOneWithoutItsAttributes(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))
	noName := copyPath
	noName.DisplayName = ""
	noID := copyPath
	noID.Identifier = ""

	for _, item := range []iterm2.ContextMenuItem{noName, noID} {
		_, err := conn.RegisterContextMenuItem(testContext(t), item)
		var api *iterm2.APIError
		assert.ErrorAs(t, err, &api)
	}
	assert.Empty(t, host.registered())
}

func TestAPersistentContextMenuItemIsRegisteredAgainOnReconnect(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	p := connectPersistent(t, srv)
	reg, err := p.RegisterContextMenuItem(testContext(t), copyPath)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	srv.CloseConnections()
	nextReconnect(t, p)
	got := host.registered()
	require.Len(t, got, 2)
	assert.Equal(t, apipb.RPCRegistrationRequest_CONTEXT_MENU, got[1].GetRole())
}
