package iterm2_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

// sessionSummary builds the summary iTerm2 sends for one pane.
func sessionSummary(id, title string) *apipb.SessionSummary {
	return &apipb.SessionSummary{
		UniqueIdentifier: proto.String(id),
		Title:            proto.String(title),
		Frame: &apipb.Frame{
			Origin: &apipb.Point{X: proto.Int32(10), Y: proto.Int32(20)},
			Size:   &apipb.Size{Width: proto.Int32(300), Height: proto.Int32(400)},
		},
		GridSize: &apipb.Size{Width: proto.Int32(80), Height: proto.Int32(24)},
	}
}

func sessionLink(id, title string) *apipb.SplitTreeNode_SplitTreeLink {
	return &apipb.SplitTreeNode_SplitTreeLink{
		Child: &apipb.SplitTreeNode_SplitTreeLink_Session{Session: sessionSummary(id, title)},
	}
}

func nodeLink(node *apipb.SplitTreeNode) *apipb.SplitTreeNode_SplitTreeLink {
	return &apipb.SplitTreeNode_SplitTreeLink{
		Child: &apipb.SplitTreeNode_SplitTreeLink_Node{Node: node},
	}
}

func TestListSessionsFlattensTheSplitTreeInLinkOrder(t *testing.T) {
	// One tab holding three panes: "a" beside a nested split of "b" over "c".
	// api.proto gives no flat pane list, so the order below is the tree's own.
	nested := &apipb.SplitTreeNode{
		Vertical: proto.Bool(false),
		Links: []*apipb.SplitTreeNode_SplitTreeLink{
			sessionLink("s-b", "b"),
			sessionLink("s-c", "c"),
		},
	}
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Number:   proto.Int32(3),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Vertical: proto.Bool(true),
					Links: []*apipb.SplitTreeNode_SplitTreeLink{
						sessionLink("s-a", "a"),
						nodeLink(nested),
					},
				},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	require.Len(t, h.Windows, 1)
	require.Len(t, h.Windows[0].Tabs, 1)
	tab := h.Windows[0].Tabs[0]

	var ids []string
	for _, s := range tab.Sessions {
		ids = append(ids, s.ID)
	}
	assert.Equal(t, []string{"s-a", "s-b", "s-c"}, ids, "panes should come out in split-tree order")

	// The tab carries no window id of its own in api.proto; it is filled in while
	// parsing so a *Tab is usable without its window.
	assert.Equal(t, "w-1", tab.WindowID)
	assert.Equal(t, int32(3), h.Windows[0].Number)
}

func TestListSessionsReadsFramesGridSizesAndTitles(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Frame: &apipb.Frame{
				Origin: &apipb.Point{X: proto.Int32(1), Y: proto.Int32(2)},
				Size:   &apipb.Size{Width: proto.Int32(1280), Height: proto.Int32(800)},
			},
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-a", "zsh")},
				},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	assert.Equal(t, iterm2.Rect{X: 1, Y: 2, Width: 1280, Height: 800}, h.Windows[0].Frame)

	s := h.Windows[0].Tabs[0].Sessions[0]
	assert.Equal(t, "zsh", s.Title)
	assert.Equal(t, iterm2.Rect{X: 10, Y: 20, Width: 300, Height: 400}, s.Frame)
	assert.Equal(t, iterm2.Size{Width: 80, Height: 24}, s.GridSize)
	assert.False(t, s.Buried)
}

func TestListSessionsReportsTabGroupsOnlyWhenTheTabIsInOne(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{
				{
					TabId:             proto.String("t-grouped"),
					TabGroupId:        proto.String("group-uuid"),
					TabGroupName:      proto.String("build"),
					TabGroupColor:     proto.String("#ff8800"),
					TabGroupCollapsed: proto.Bool(true),
				},
				{TabId: proto.String("t-loose")},
			},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	grouped := h.Tab("t-grouped")
	require.NotNil(t, grouped)
	require.NotNil(t, grouped.Group)
	assert.Equal(t, "group-uuid", grouped.Group.ID)
	assert.Equal(t, "build", grouped.Group.Name)
	assert.Equal(t, "#ff8800", grouped.Group.Color)
	assert.True(t, grouped.Group.Collapsed)

	// api.proto: the other three fields are meaningful only when tab_group_id is
	// set, so an unset id must not produce an empty group the caller might read.
	loose := h.Tab("t-loose")
	require.NotNil(t, loose)
	assert.Nil(t, loose.Group)
}

func TestListSessionsReportsBuriedSessionsWithoutAFrame(t *testing.T) {
	buried := &apipb.SessionSummary{
		UniqueIdentifier: proto.String("s-buried"),
		Title:            proto.String("held"),
	}
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		BuriedSessions: []*apipb.SessionSummary{buried},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	require.Len(t, h.BuriedSessions, 1)
	s := h.BuriedSessions[0]
	assert.True(t, s.Buried)
	assert.Equal(t, "held", s.Title)
	// api.proto: frame and grid size "will not be set for buried sessions".
	assert.Equal(t, iterm2.Rect{}, s.Frame)
	assert.Equal(t, iterm2.Size{}, s.GridSize)

	// A buried session belongs to no tab, so it is reachable only through the flat
	// view and the lookup.
	assert.Equal(t, s, h.Session("s-buried"))
	assert.Len(t, h.Sessions(), 1)
}

func TestHierarchyLookupsAndActiveSession(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId:      proto.String("w-1"),
			SelectedTabId: proto.String("t-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId:           proto.String("t-1"),
				ActiveSessionId: proto.String("s-b"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{
						sessionLink("s-a", "a"),
						sessionLink("s-b", "b"),
					},
				},
				MinimizedSessions: []*apipb.SessionSummary{sessionSummary("s-min", "minimized")},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	assert.Equal(t, "t-1", h.Windows[0].SelectedTabID)

	tab := h.Tab("t-1")
	require.NotNil(t, tab)
	active := tab.ActiveSession()
	require.NotNil(t, active)
	assert.Equal(t, "s-b", active.ID)

	// A minimized pane is not among Sessions, but is still in the flat view.
	require.Len(t, tab.Sessions, 2)
	require.Len(t, tab.MinimizedSessions, 1)
	assert.Len(t, h.Sessions(), 3)

	require.NotNil(t, h.Window("w-1"))
	assert.Nil(t, h.Window("w-absent"))
	assert.Nil(t, h.Tab("t-absent"))
	assert.Nil(t, h.Session("s-absent"))
}

func TestActiveSessionIsNilWhenITerm2DidNotSayWhichItIs(t *testing.T) {
	// An iTerm2 older than protocol 1.18 sends no active_session_id.
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId: proto.String("t-1"),
				Root: &apipb.SplitTreeNode{
					Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-a", "a")},
				},
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)
	assert.Nil(t, h.Tab("t-1").ActiveSession())
}

func TestListSessionsReadsTmuxIdentifiers(t *testing.T) {
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs: []*apipb.ListSessionsResponse_Tab{{
				TabId:            proto.String("t-1"),
				TmuxWindowId:     proto.String("@7"),
				TmuxConnectionId: proto.String("conn-1"),
			}},
		}},
	}))
	conn := connect(t, srv)

	h, err := conn.ListSessions(testContext(t))
	require.NoError(t, err)

	tab := h.Tab("t-1")
	require.NotNil(t, tab)
	assert.Equal(t, "@7", tab.TmuxWindowID)
	assert.Equal(t, "conn-1", tab.TmuxConnectionID)
}

func TestListSessionsRejectsAResponseWithNoBody(t *testing.T) {
	// The default fake handler answers every request with an empty message, which
	// is not a ListSessionsResponse. Guessing at an empty hierarchy would hide
	// the mismatch.
	srv := startFake(t)
	conn := connect(t, srv)

	_, err := conn.ListSessions(testContext(t))
	var apiErr *iterm2.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "list_sessions_response")
}

func TestVariableScopeNamesMatchApiProto(t *testing.T) {
	assert.Equal(t, "SESSION", iterm2.ScopeSession.String())
	assert.Equal(t, "TAB", iterm2.ScopeTab.String())
	assert.Equal(t, "WINDOW", iterm2.ScopeWindow.String())
	assert.Equal(t, "APP", iterm2.ScopeApp.String())

	// api.proto numbers the scopes from 1, so no scope may share the Go zero
	// value — a caller who forgot to set one must not silently get SESSION.
	assert.NotEqual(t, iterm2.VariableScope(0), iterm2.ScopeSession)
}
