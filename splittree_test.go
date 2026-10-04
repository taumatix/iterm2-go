package iterm2_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

func listOneTab(t *testing.T, root *apipb.SplitTreeNode) *iterm2.Tab {
	t.Helper()
	srv := startFake(t, answerListSessions(&apipb.ListSessionsResponse{
		Windows: []*apipb.ListSessionsResponse_Window{{
			WindowId: proto.String("w-1"),
			Tabs:     []*apipb.ListSessionsResponse_Tab{{TabId: proto.String("t-1"), Root: root}},
		}},
	}))
	h, err := connect(t, srv).ListSessions(testContext(t))
	require.NoError(t, err)
	require.Len(t, h.Windows, 1)
	require.Len(t, h.Windows[0].Tabs, 1)
	return h.Windows[0].Tabs[0]
}

// "a" beside a split of "b" over "c": the shape Tab.Sessions flattens away.
func TestSplitTreeKeepsTheArrangementOfPanes(t *testing.T) {
	tab := listOneTab(t, &apipb.SplitTreeNode{
		Vertical: proto.Bool(true),
		Links: []*apipb.SplitTreeNode_SplitTreeLink{
			sessionLink("s-a", "a"),
			nodeLink(&apipb.SplitTreeNode{
				Vertical: proto.Bool(false),
				Links: []*apipb.SplitTreeNode_SplitTreeLink{
					sessionLink("s-b", "b"),
					sessionLink("s-c", "c"),
				},
			}),
		},
	})

	root := tab.SplitTree()
	require.NotNil(t, root)
	assert.Nil(t, root.Session, "the root is a split, not a pane")
	assert.True(t, root.Vertical, "a vertical divider: a sits beside the rest")
	require.Len(t, root.Children, 2)

	left := root.Children[0]
	require.NotNil(t, left.Session)
	assert.Same(t, tab.Sessions[0], left.Session, "a pane in the tree must be the same *Session as in Sessions")
	assert.Empty(t, left.Children)

	right := root.Children[1]
	assert.Nil(t, right.Session)
	assert.False(t, right.Vertical, "a horizontal divider: b above c")
	require.Len(t, right.Children, 2)
	assert.Same(t, tab.Sessions[1], right.Children[0].Session)
	assert.Same(t, tab.Sessions[2], right.Children[1].Session)
	assert.Equal(t, "s-c", right.Children[1].Session.ID)
}

// A tab with one pane is still a split node holding it, as api.proto sends it.
func TestSplitTreeOfASinglePane(t *testing.T) {
	tab := listOneTab(t, &apipb.SplitTreeNode{
		Links: []*apipb.SplitTreeNode_SplitTreeLink{sessionLink("s-only", "only")},
	})
	root := tab.SplitTree()
	require.NotNil(t, root)
	require.Len(t, root.Children, 1)
	assert.Same(t, tab.Sessions[0], root.Children[0].Session)
}

// A link with neither child is a kind this build does not know; it is left out
// of the tree just as it is left out of Sessions, so the two always agree.
func TestSplitTreeSkipsALinkItDoesNotUnderstand(t *testing.T) {
	tab := listOneTab(t, &apipb.SplitTreeNode{
		Vertical: proto.Bool(true),
		Links: []*apipb.SplitTreeNode_SplitTreeLink{
			{},
			sessionLink("s-a", "a"),
		},
	})
	root := tab.SplitTree()
	require.Len(t, root.Children, 1)
	assert.Same(t, tab.Sessions[0], root.Children[0].Session)
}

func TestSplitTreeIsNilWhenITerm2SentNone(t *testing.T) {
	tab := listOneTab(t, nil)
	assert.Nil(t, tab.SplitTree())
	assert.Empty(t, tab.Sessions)
}

// Panes walks the tree and must give exactly Sessions, in the same order.
func TestSplitTreePanesMatchSessions(t *testing.T) {
	tab := listOneTab(t, &apipb.SplitTreeNode{
		Links: []*apipb.SplitTreeNode_SplitTreeLink{
			nodeLink(&apipb.SplitTreeNode{Vertical: proto.Bool(true), Links: []*apipb.SplitTreeNode_SplitTreeLink{
				sessionLink("s-1", "1"), sessionLink("s-2", "2"),
			}}),
			sessionLink("s-3", "3"),
		},
	})
	assert.Equal(t, tab.Sessions, tab.SplitTree().Panes())
}
