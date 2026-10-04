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

var gitBranchTitle = iterm2.TitleProvider{
	RPC: iterm2.RPC{
		Name:      "branch_title",
		Arguments: []string{"branch"},
		Defaults:  map[string]string{"branch": "user.gitBranch"},
		Handler: func(_ context.Context, args map[string]json.RawMessage) (any, error) {
			var branch string
			_ = json.Unmarshal(args["branch"], &branch)
			return "⎇ " + branch, nil
		},
	},
	DisplayName: "Git branch",
	Identifier:  "com.example.branch-title",
}

// A title provider is an RPC registered in the SESSION_TITLE role, with the
// attributes iTerm2 lists it under in Preferences.
func TestRegisterTitleProviderSendsTheRoleAndItsAttributes(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))

	reg, err := conn.RegisterTitleProvider(testContext(t), gitBranchTitle)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	got := host.registered()
	require.Len(t, got, 1)
	assert.Equal(t, "branch_title", got[0].GetName())
	assert.Equal(t, apipb.RPCRegistrationRequest_SESSION_TITLE, got[0].GetRole())
	attrs := got[0].GetSessionTitleAttributes()
	require.NotNil(t, attrs)
	assert.Equal(t, "Git branch", attrs.GetDisplayName())
	assert.Equal(t, "com.example.branch-title", attrs.GetUniqueIdentifier())
	require.Len(t, got[0].GetDefaults(), 1)
	assert.Equal(t, "user.gitBranch", got[0].GetDefaults()[0].GetPath())
}

func TestATitleProviderAnswersWithItsTitle(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	reg, err := conn.RegisterTitleProvider(testContext(t), gitBranchTitle)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	require.NoError(t, srv.Notify(testContext(t), call("t-1", "branch_title", map[string]string{"branch": `"main"`})))
	assert.JSONEq(t, `"⎇ main"`, host.result(t).GetJsonValue())
}

// iTerm2 shows the result as the title, so anything but a string is the
// handler's mistake, reported back as one rather than shown as JSON.
func TestATitleProviderThatReturnsANonStringReportsAnError(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	wrong := gitBranchTitle
	wrong.Handler = func(context.Context, map[string]json.RawMessage) (any, error) { return 42, nil }
	reg, err := conn.RegisterTitleProvider(testContext(t), wrong)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	require.NoError(t, srv.Notify(testContext(t), call("t-2", "branch_title", nil)))
	r := host.result(t)
	assert.Empty(t, r.GetJsonValue())
	assert.Contains(t, r.GetJsonException(), "string")
}

func TestRegisterTitleProviderRefusesOneWithoutItsAttributes(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))
	noName := gitBranchTitle
	noName.DisplayName = ""
	noID := gitBranchTitle
	noID.Identifier = ""

	for _, tp := range []iterm2.TitleProvider{noName, noID} {
		_, err := conn.RegisterTitleProvider(testContext(t), tp)
		var api *iterm2.APIError
		assert.ErrorAs(t, err, &api)
	}
	assert.Empty(t, host.registered())
}

func TestAPersistentTitleProviderIsRegisteredAgainOnReconnect(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	p := connectPersistent(t, srv)
	reg, err := p.RegisterTitleProvider(testContext(t), gitBranchTitle)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	srv.CloseConnections()
	nextReconnect(t, p)
	got := host.registered()
	require.Len(t, got, 2)
	assert.Equal(t, apipb.RPCRegistrationRequest_SESSION_TITLE, got[1].GetRole())
}
