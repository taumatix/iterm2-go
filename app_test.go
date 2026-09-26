package iterm2_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// recorder captures the requests the client sends and answers each with a
// canned response, so a test can assert on what went onto the wire.
type recorder struct {
	mu       sync.Mutex
	requests []*apipb.ClientOriginatedMessage
	reply    *apipb.ServerOriginatedMessage
}

func newRecorder(reply *apipb.ServerOriginatedMessage) *recorder {
	return &recorder{reply: reply}
}

func (r *recorder) handler() fakeiterm.Handler {
	return func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		r.mu.Lock()
		r.requests = append(r.requests, proto.Clone(req).(*apipb.ClientOriginatedMessage))
		r.mu.Unlock()
		// Cloned so that stamping the id onto one reply does not affect the next.
		return proto.Clone(r.reply).(*apipb.ServerOriginatedMessage)
	}
}

func (r *recorder) option() fakeiterm.Option {
	return fakeiterm.WithHandler(r.handler())
}

// last returns the most recent request, failing the test if there is none.
func (r *recorder) last(t *testing.T) *apipb.ClientOriginatedMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.requests, "no request reached the fake iTerm2")
	return r.requests[len(r.requests)-1]
}

func TestSendTextEncodesTheRequestITerm2Expects(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_SendTextResponse{
			SendTextResponse: &apipb.SendTextResponse{Status: apipb.SendTextResponse_OK.Enum()},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	require.NoError(t, conn.SendText(testContext(t), "s-1", "echo hi\n", true))

	got := rec.last(t).GetSendTextRequest()
	require.NotNil(t, got)
	assert.Equal(t, "s-1", got.GetSession())
	assert.Equal(t, "echo hi\n", got.GetText())
	assert.True(t, got.GetSuppressBroadcast())
}

func TestSendTextReportsSessionNotFoundAsStatusError(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_SendTextResponse{
			SendTextResponse: &apipb.SendTextResponse{
				Status: apipb.SendTextResponse_SESSION_NOT_FOUND.Enum(),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	err := conn.SendText(testContext(t), "s-gone", "hi", false)

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "SendText", statusErr.Op)
	// The status is reported with api.proto's own spelling so it can be looked up.
	assert.Equal(t, "SESSION_NOT_FOUND", statusErr.Status)
	assert.Equal(t, int32(1), statusErr.Code)
}

func TestSendTextRejectsAnEmptySession(t *testing.T) {
	conn := connect(t, startFake(t))
	assert.Error(t, conn.SendText(testContext(t), "", "hi", false))
}

func TestSessionSendTextAddressesItsOwnSession(t *testing.T) {
	// The session must come from a real ListSessions, so that the connection it
	// sends through is the one the parser attached rather than one the test chose.
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-42", "zsh")},
				},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_SendTextResponse{
			SendTextResponse: &apipb.SendTextResponse{},
		},
	})
	srv.SetHandler(rec.handler())

	session := h.Session("s-42")
	require.NotNil(t, session)
	require.NoError(t, session.SendText(testContext(t), "ls\n"))

	assert.Equal(t, "s-42", rec.last(t).GetSendTextRequest().GetSession())
}

func TestCreateTabEncodesEveryOption(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_CreateTabResponse{
			CreateTabResponse: &apipb.CreateTabResponse{
				Status:    apipb.CreateTabResponse_OK.Enum(),
				WindowId:  proto.String("w-9"),
				TabId:     proto.Int32(4),
				SessionId: proto.String("s-new"),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	index := uint32(2)
	background := false
	tab, err := conn.CreateTab(testContext(t), iterm2.CreateTabOptions{
		ProfileName:             "Remote",
		WindowID:                "w-9",
		TabIndex:                &index,
		SelectTab:               &background,
		CustomProfileProperties: map[string]string{"Command": `"/bin/zsh"`},
	})
	require.NoError(t, err)

	assert.Equal(t, "w-9", tab.WindowID)
	// api.proto types this field as int32 here and as a string everywhere else.
	assert.Equal(t, "4", tab.TabID)
	assert.Equal(t, "s-new", tab.SessionID)

	got := rec.last(t).GetCreateTabRequest()
	require.NotNil(t, got)
	assert.Equal(t, "Remote", got.GetProfileName())
	assert.Equal(t, "w-9", got.GetWindowId())
	assert.Equal(t, uint32(2), got.GetTabIndex())
	assert.False(t, got.GetSelectTab())
	require.Len(t, got.GetCustomProfileProperties(), 1)
	assert.Equal(t, "Command", got.GetCustomProfileProperties()[0].GetKey())
	assert.Equal(t, `"/bin/zsh"`, got.GetCustomProfileProperties()[0].GetJsonValue())
}

func TestCreateTabLeavesUnsetOptionsUnset(t *testing.T) {
	// An unset profile name means "the default profile", and an unset select_tab
	// means "iTerm2's default". Sending empty values instead would override both.
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_CreateTabResponse{
			CreateTabResponse: &apipb.CreateTabResponse{},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.CreateTab(testContext(t), iterm2.CreateTabOptions{})
	require.NoError(t, err)

	got := rec.last(t).GetCreateTabRequest()
	require.NotNil(t, got)
	assert.Nil(t, got.ProfileName)
	assert.Nil(t, got.WindowId)
	assert.Nil(t, got.TabIndex)
	assert.Nil(t, got.SelectTab)
	assert.Empty(t, got.CustomProfileProperties)
}

func TestCreateTabRefusesATabIndexWithoutAWindow(t *testing.T) {
	// api.proto allows tab_index only with window_id, and iTerm2 answers
	// INVALID_TAB_INDEX having already created the tab. Refusing locally avoids
	// that half-done outcome.
	rec := newRecorder(&apipb.ServerOriginatedMessage{})
	conn := connect(t, startFake(t, rec.option()))

	index := uint32(1)
	_, err := conn.CreateTab(testContext(t), iterm2.CreateTabOptions{TabIndex: &index})
	require.Error(t, err)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.requests, "nothing should have been sent")
}

func TestCreateTabReportsAnInvalidProfileAsStatusError(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_CreateTabResponse{
			CreateTabResponse: &apipb.CreateTabResponse{
				Status: apipb.CreateTabResponse_INVALID_PROFILE_NAME.Enum(),
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	_, err := conn.CreateTab(testContext(t), iterm2.CreateTabOptions{ProfileName: "nope"})

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "INVALID_PROFILE_NAME", statusErr.Status)
}

func TestSplitPaneEncodesDirectionAndReturnsNewSessions(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_SplitPaneResponse{
			SplitPaneResponse: &apipb.SplitPaneResponse{
				Status:    apipb.SplitPaneResponse_OK.Enum(),
				SessionId: []string{"s-new"},
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	ids, err := conn.SplitPane(testContext(t), "s-1", iterm2.SplitPaneOptions{
		Direction: iterm2.SplitHorizontal,
		Before:    true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s-new"}, ids)

	got := rec.last(t).GetSplitPaneRequest()
	require.NotNil(t, got)
	assert.Equal(t, "s-1", got.GetSession())
	assert.Equal(t, apipb.SplitPaneRequest_HORIZONTAL, got.GetSplitDirection())
	assert.True(t, got.GetBefore())
}

func TestSplitPaneReturnsTheSessionsItDidCreateAlongsideCannotSplit(t *testing.T) {
	// api.proto: splitting several sessions reports CANNOT_SPLIT if any failed,
	// even when others succeeded, so the ids must not be thrown away with the
	// error.
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_SplitPaneResponse{
			SplitPaneResponse: &apipb.SplitPaneResponse{
				Status:    apipb.SplitPaneResponse_CANNOT_SPLIT.Enum(),
				SessionId: []string{"s-did-split"},
			},
		},
	})
	conn := connect(t, startFake(t, rec.option()))

	ids, err := conn.SplitPane(testContext(t), iterm2.SessionAll, iterm2.SplitPaneOptions{})

	var statusErr *iterm2.StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, "CANNOT_SPLIT", statusErr.Status)
	assert.Equal(t, []string{"s-did-split"}, ids, "the sessions that did split must still be reported")
}

func TestActivateEncodesTheIdentifierAndOptions(t *testing.T) {
	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_ActivateResponse{
			ActivateResponse: &apipb.ActivateResponse{Status: apipb.ActivateResponse_OK.Enum()},
		},
	})
	srv := startFake(t, rec.option())
	conn := connect(t, srv)

	// App activation with no identifier: api.proto says omit it to activate the
	// app without changing anything else.
	require.NoError(t, conn.Activate(testContext(t), iterm2.ActivateOptions{
		ActivateApp:     true,
		RaiseAllWindows: true,
	}))
	got := rec.last(t).GetActivateRequest()
	require.NotNil(t, got)
	assert.Nil(t, got.GetIdentifier())
	require.NotNil(t, got.GetActivateApp())
	assert.True(t, got.GetActivateApp().GetRaiseAllWindows())
	assert.False(t, got.GetActivateApp().GetIgnoringOtherApps())
}

func TestActivateAddressesSessionsTabsAndWindowsSeparately(t *testing.T) {
	hierarchy := &apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-1", "zsh")},
				},
			}},
		}},
	}
	srv := startFake(t, answerListSessions(hierarchy))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	rec := newRecorder(&apipb.ServerOriginatedMessage{
		Submessage: &apipb.ServerOriginatedMessage_ActivateResponse{
			ActivateResponse: &apipb.ActivateResponse{},
		},
	})
	srv.SetHandler(rec.handler())

	require.NoError(t, h.Session("s-1").Activate(testContext(t), iterm2.ActivateOptions{
		SelectTab:        true,
		SelectSession:    true,
		OrderWindowFront: true,
	}))
	sessionReq := rec.last(t).GetActivateRequest()
	assert.Equal(t, "s-1", sessionReq.GetSessionId())
	assert.True(t, sessionReq.GetSelectTab())
	assert.True(t, sessionReq.GetSelectSession())
	assert.True(t, sessionReq.GetOrderWindowFront())

	require.NoError(t, h.Tab("t-1").Activate(testContext(t), iterm2.ActivateOptions{SelectTab: true}))
	assert.Equal(t, "t-1", rec.last(t).GetActivateRequest().GetTabId())

	require.NoError(t, h.Window("w-1").Activate(testContext(t), iterm2.ActivateOptions{}))
	assert.Equal(t, "w-1", rec.last(t).GetActivateRequest().GetWindowId())
}
