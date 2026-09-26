package iterm2_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

func closeReply(statuses ...apipb.CloseResponse_Status) *apipb.ServerOriginatedMessage {
	return &apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_CloseResponse{
			CloseResponse: &apipb.CloseResponse{Statuses: statuses},
		},
	}
}

func TestCloseSessionsEncodesTheTargetsAndForce(t *testing.T) {
	rec := newRecorder(closeReply(apipb.CloseResponse_OK, apipb.CloseResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	require.NoError(t, conn.CloseSessions(testContext(t), true, "s-1", "s-2"))

	req := rec.last(t).GetCloseRequest()
	require.NotNil(t, req)
	assert.Equal(t, []string{"s-1", "s-2"}, req.GetSessions().GetSessionIds())
	assert.True(t, req.GetForce())
}

func TestCloseTabsAndWindowsUseTheirOwnTargets(t *testing.T) {
	rec := newRecorder(closeReply(apipb.CloseResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	require.NoError(t, conn.CloseTabs(testContext(t), false, "t-1"))
	assert.Equal(t, []string{"t-1"}, rec.last(t).GetCloseRequest().GetTabs().GetTabIds())

	require.NoError(t, conn.CloseWindows(testContext(t), false, "w-1"))
	assert.Equal(t, []string{"w-1"}, rec.last(t).GetCloseRequest().GetWindows().GetWindowIds())
}

func TestCloseNamesTheTargetThatFailed(t *testing.T) {
	// api.proto gives one status per target with no id attached, so a partial
	// failure is only useful if the id is put back on.
	rec := newRecorder(closeReply(
		apipb.CloseResponse_OK,
		apipb.CloseResponse_USER_DECLINED,
		apipb.CloseResponse_NOT_FOUND,
	))
	conn := connect(t, startFake(t, rec.option()))

	err := conn.CloseSessions(testContext(t), false, "s-ok", "s-declined", "s-gone")
	require.Error(t, err)

	assert.Contains(t, err.Error(), "s-declined")
	assert.Contains(t, err.Error(), "USER_DECLINED")
	assert.Contains(t, err.Error(), "s-gone")
	assert.Contains(t, err.Error(), "NOT_FOUND")
	assert.NotContains(t, err.Error(), "s-ok", "the target that closed should not be reported")

	var statusErr *iterm2.StatusError
	assert.ErrorAs(t, err, &statusErr)
}

func TestCloseRejectsAStatusCountItCannotPairUp(t *testing.T) {
	// Pairing is positional, so reporting the wrong id as having failed would be
	// worse than refusing to guess.
	rec := newRecorder(closeReply(apipb.CloseResponse_OK))
	conn := connect(t, startFake(t, rec.option()))

	err := conn.CloseSessions(testContext(t), false, "s-1", "s-2")

	var apiErr *iterm2.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "2")
}

func TestCloseWithNoTargetsSendsNothing(t *testing.T) {
	rec := newRecorder(closeReply())
	conn := connect(t, startFake(t, rec.option()))

	assert.Error(t, conn.CloseSessions(testContext(t), false))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.requests)
}
