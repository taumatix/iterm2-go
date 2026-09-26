package iterm2_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

func variableReply(status apipb.VariableResponse_Status, values ...string) *apipb.ServerOriginatedMessage {
	return &apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_VariableResponse{
			VariableResponse: &apipb.VariableResponse{
				Status: status.Enum(),
				Values: values,
			},
		},
	}
}

func TestGetVariablesPairsValuesWithTheNamesAsked(t *testing.T) {
	// api.proto: values is 1:1 with get, so the pairing is positional.
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"zsh"`, `42`))
	conn := connect(t, startFake(t, rec.option()))

	got, err := conn.GetVariables(testContext(t), iterm2.ScopeSession, "s-1", "jobName", "pid")
	require.NoError(t, err)

	assert.JSONEq(t, `"zsh"`, string(got["jobName"]))
	assert.JSONEq(t, `42`, string(got["pid"]))

	req := rec.last(t).GetVariableRequest()
	require.NotNil(t, req)
	assert.Equal(t, "s-1", req.GetSessionId())
	assert.Equal(t, []string{"jobName", "pid"}, req.GetGet())
}

func TestGetVariablesOmitsUnsetVariables(t *testing.T) {
	// api.proto: unset variables come back as JSON null. Leaving them out lets a
	// caller tell "unset" from "set" with a map lookup.
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"zsh"`, `null`))
	conn := connect(t, startFake(t, rec.option()))

	got, err := conn.GetVariables(testContext(t), iterm2.ScopeSession, "s-1", "jobName", "missing")
	require.NoError(t, err)

	assert.Len(t, got, 1)
	_, present := got["missing"]
	assert.False(t, present, "an unset variable should not appear in the map")
}

func TestGetVariablesRejectsAMismatchedValueCount(t *testing.T) {
	// Pairing up a short list would silently attach values to the wrong names.
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"only-one"`))
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.GetVariables(testContext(t), iterm2.ScopeSession, "s-1", "a", "b")

	var apiErr *iterm2.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "asked for 2")
}

func TestGetVariablesWithNoNamesSendsNothing(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	got, err := conn.GetVariables(testContext(t), iterm2.ScopeSession, "s-1")
	require.NoError(t, err)
	assert.Empty(t, got)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.requests, "an empty request is a round trip worth skipping")
}

func TestGetVariablesSetsTheScopeOneofPerScope(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"v"`))
	conn := connect(t, startFake(t, rec.option()))
	ctx := testContext(t)

	_, err := conn.GetVariables(ctx, iterm2.ScopeTab, "t-1", "x")
	require.NoError(t, err)
	assert.Equal(t, "t-1", rec.last(t).GetVariableRequest().GetTabId())

	_, err = conn.GetVariables(ctx, iterm2.ScopeWindow, "w-1", "x")
	require.NoError(t, err)
	assert.Equal(t, "w-1", rec.last(t).GetVariableRequest().GetWindowId())

	// The app scope has no identifier; api.proto expresses it as a bool.
	_, err = conn.GetVariables(ctx, iterm2.ScopeApp, "", "x")
	require.NoError(t, err)
	assert.True(t, rec.last(t).GetVariableRequest().GetApp())
}

func TestGetVariablesRefusesAnUnsetScope(t *testing.T) {
	// api.proto numbers the scopes from 1, so the Go zero value is not a scope.
	// Defaulting it would read the wrong object.
	rec := newRecorder(variableReply(apipb.VariableResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.GetVariables(testContext(t), iterm2.VariableScope(0), "s-1", "x")
	require.Error(t, err)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.requests)
}

func TestGetVariablesRefusesAnEmptyIdentifierOutsideAppScope(t *testing.T) {
	// iTerm2 would read the empty string as a session id and answer
	// SESSION_NOT_FOUND, which reads as "your session went away" rather than
	// "you did not name one".
	conn := connect(t, startFake(t))

	for _, scope := range []iterm2.VariableScope{iterm2.ScopeSession, iterm2.ScopeTab, iterm2.ScopeWindow} {
		_, err := conn.GetVariables(testContext(t), scope, "", "x")
		assert.Error(t, err, "scope %s with no identifier", scope)
	}
}

func TestGetStringVariableDecodesTheJSON(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"/Users/me"`))
	conn := connect(t, startFake(t, rec.option()))

	value, ok, err := conn.GetStringVariable(testContext(t), iterm2.ScopeSession, "s-1", "path")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "/Users/me", value)
}

func TestGetStringVariableReportsUnsetWithoutAnError(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `null`))
	conn := connect(t, startFake(t, rec.option()))

	value, ok, err := conn.GetStringVariable(testContext(t), iterm2.ScopeSession, "s-1", "nope")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, value)
}

func TestGetStringVariableRefusesANonStringValue(t *testing.T) {
	// Rendering a number as its JSON text would hide the type mismatch.
	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `42`))
	conn := connect(t, startFake(t, rec.option()))

	_, _, err := conn.GetStringVariable(testContext(t), iterm2.ScopeSession, "s-1", "pid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a string")
}

func TestSetVariablesEncodesEachSet(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	require.NoError(t, conn.SetVariables(testContext(t), iterm2.ScopeSession, "s-1", map[string]string{
		"user.host": `"build-box"`,
	}))

	req := rec.last(t).GetVariableRequest()
	require.NotNil(t, req)
	require.Len(t, req.GetSet(), 1)
	assert.Equal(t, "user.host", req.GetSet()[0].GetName())
	assert.Equal(t, `"build-box"`, req.GetSet()[0].GetValue())
	assert.Empty(t, req.GetGet())
}

func TestSetStringVariableQuotesTheValue(t *testing.T) {
	rec := newRecorder(variableReply(apipb.VariableResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	require.NoError(t, conn.SetStringVariable(testContext(t), iterm2.ScopeSession, "s-1",
		"user.note", `he said "hi"`))

	value := rec.last(t).GetVariableRequest().GetSet()[0].GetValue()
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(value), &decoded),
		"the value must be valid JSON, not a bare string")
	assert.Equal(t, `he said "hi"`, decoded)
}

func TestSetVariablesReportsInvalidNameAsStatusError(t *testing.T) {
	// iTerm2 requires names a script sets to begin with "user.".
	rec := newRecorder(variableReply(apipb.VariableResponse_INVALID_NAME))
	conn := connect(t, startFake(t, rec.option()))

	err := conn.SetVariables(testContext(t), iterm2.ScopeSession, "s-1", map[string]string{
		"jobName": `"mine"`,
	})

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "INVALID_NAME", statusErr.Status)
}

func TestGetVariablesReportsMultiGetDisallowed(t *testing.T) {
	// iTerm2 refuses to read several variables from "all" at once.
	rec := newRecorder(variableReply(apipb.VariableResponse_MULTI_GET_DISALLOWED))
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.GetVariables(testContext(t), iterm2.ScopeSession, iterm2.SessionAll, "a", "b")

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "MULTI_GET_DISALLOWED", statusErr.Status)
}

func TestSessionVariableHelpersUseTheSessionScope(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-7", "zsh")},
				},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	rec := newRecorder(variableReply(apipb.VariableResponse_OK, `"zsh"`))
	srv.SetHandler(rec.handler())

	session := h.Session("s-7")
	require.NotNil(t, session)

	value, ok, err := session.Variable(testContext(t), "jobName")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "zsh", value)
	assert.Equal(t, "s-7", rec.last(t).GetVariableRequest().GetSessionId())

	require.NoError(t, session.SetVariable(testContext(t), "user.x", "y"))
	assert.Equal(t, "s-7", rec.last(t).GetVariableRequest().GetSessionId())
}

func TestAllVariablesIsApiProtoSpecialName(t *testing.T) {
	assert.Equal(t, "*", iterm2.AllVariables)
}
