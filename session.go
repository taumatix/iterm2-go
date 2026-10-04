package iterm2

import (
	"github.com/taumatix/iterm2-go/apipb"
)

// Session identifiers that iTerm2 accepts in place of a real one, documented at
// the top of api.proto. Not every request accepts them; the comment on each
// request message in api.proto says which do.
const (
	// SessionAll addresses every session at once.
	SessionAll = "all"

	// SessionActive addresses whichever session currently has keyboard focus.
	SessionActive = "active"
)

// VariableScope selects which object a variable belongs to.
//
// The values mirror api.proto's VariableScope enum, which starts at 1 — there
// is no zero value, so a VariableScope left at its Go zero is invalid and the
// methods taking one reject it rather than defaulting to a scope the caller did
// not choose.
type VariableScope int32

const (
	ScopeSession VariableScope = VariableScope(apipb.VariableScope_SESSION)
	ScopeTab     VariableScope = VariableScope(apipb.VariableScope_TAB)
	ScopeWindow  VariableScope = VariableScope(apipb.VariableScope_WINDOW)
	ScopeApp     VariableScope = VariableScope(apipb.VariableScope_APP)
)

// String names the scope as api.proto spells it.
func (s VariableScope) String() string {
	if name := apipb.VariableScope(s).String(); name != "" {
		return name
	}
	return "UNKNOWN"
}

// valid reports whether s is one of the four scopes api.proto defines.
func (s VariableScope) valid() bool {
	switch s {
	case ScopeSession, ScopeTab, ScopeWindow, ScopeApp:
		return true
	default:
		return false
	}
}

// Size is a width and height in character cells.
type Size struct {
	Width  int32
	Height int32
}

// Rect is a position and size in screen points, as iTerm2 reports window and
// pane frames.
type Rect struct {
	X      int32
	Y      int32
	Width  int32
	Height int32
}

// Hierarchy is a snapshot of every window, tab and session iTerm2 had open when
// [Conn.ListSessions] was called.
//
// It is a snapshot, not a live view: sessions open and close, so an id taken
// from here can be gone by the time it is used. Requests naming a vanished
// session come back as a [*StatusError] with a NOT_FOUND status.
type Hierarchy struct {
	Windows []*Window

	// BuriedSessions are sessions iTerm2 is holding without a tab. They have no
	// frame or grid size, and no tab or window to reach them through.
	BuriedSessions []*Session
}

// Window is one iTerm2 window.
type Window struct {
	conn api

	ID     string
	Number int32
	Frame  Rect

	// SelectedTabID is the tab on top. iTerm2 began reporting it in protocol
	// 1.18 and leaves it empty on older versions.
	SelectedTabID string

	Tabs []*Tab
}

// Tab is one tab within a window.
type Tab struct {
	conn api

	ID string

	// WindowID is the window this tab was found in. api.proto does not carry it
	// on the tab; it is filled in from the enclosing window while parsing, so
	// that a *Tab is usable on its own.
	WindowID string

	// ActiveSessionID is the pane with focus within this tab. Reported from
	// protocol 1.18; empty on older versions.
	ActiveSessionID string

	// TmuxWindowID and TmuxConnectionID are set only for a tab belonging to a
	// tmux integration session.
	TmuxWindowID     string
	TmuxConnectionID string

	// Group describes the tab group this tab belongs to, or nil when it belongs
	// to none. Reported from protocol 1.19.
	Group *TabGroup

	// Sessions are the tab's panes, flattened from api.proto's split tree in
	// left-to-right, top-to-bottom order. [Tab.SplitTree] keeps the tree's
	// shape.
	Sessions []*Session

	splitTree *SplitNode

	// MinimizedSessions are panes the user has collapsed. They are not in
	// Sessions.
	MinimizedSessions []*Session
}

// SplitTree returns how the tab's panes are arranged, or nil when iTerm2 sent
// no layout for it. Its panes are the same *Session values as in Sessions.
func (t *Tab) SplitTree() *SplitNode { return t.splitTree }

// SplitNode is one node of a tab's split tree, as api.proto's SplitTreeNode
// lays it out: either a pane, or a divided area holding further nodes.
//
// It carries no geometry of its own. Each pane's Frame and GridSize are on its
// Session, and anything more (proportions, divider positions) is not something
// iTerm2 reports.
type SplitNode struct {
	// Session is set when this node is a pane, and nil when it is a split.
	Session *Session

	// Vertical is the direction of this split's dividers: true when they are
	// vertical, so Children sit side by side, left to right; false when they
	// are horizontal, so Children are stacked, top to bottom.
	Vertical bool

	// Children are the areas this split divides, in order. Empty for a pane.
	Children []*SplitNode
}

// Panes returns the panes under n, depth first in link order, which is the
// order [Tab.Sessions] has.
func (n *SplitNode) Panes() []*Session {
	if n == nil {
		return nil
	}
	if n.Session != nil {
		return []*Session{n.Session}
	}
	var out []*Session
	for _, c := range n.Children {
		out = append(out, c.Panes()...)
	}
	return out
}

// TabGroup is the group a tab belongs to.
//
// A group has no central record in iTerm2: its identity is a UUID carried by
// each member tab, and every member reports the same name, colour and collapsed
// state. Two tabs are in the same group exactly when their IDs match.
type TabGroup struct {
	ID string

	Name string

	// Color is a hex string as iTerm2 produces it: "#rrggbb" for sRGB, or
	// "p3#rrggbbrrggbb" for Display P3.
	Color string

	Collapsed bool
}

// Session is one pane.
type Session struct {
	conn api

	ID    string
	Title string

	// Frame and GridSize are unset for a buried session, because it is not on
	// screen to have either.
	Frame    Rect
	GridSize Size

	// Buried reports whether iTerm2 is holding this session without a tab.
	Buried bool
}

// Sessions returns every session in the hierarchy, buried ones last.
//
// It is the flat view most callers want: finding a session by its title or a
// variable does not usually care which window it is in.
func (h *Hierarchy) Sessions() []*Session {
	var out []*Session
	for _, w := range h.Windows {
		for _, t := range w.Tabs {
			out = append(out, t.Sessions...)
			out = append(out, t.MinimizedSessions...)
		}
	}
	return append(out, h.BuriedSessions...)
}

// Tabs returns every tab in the hierarchy.
func (h *Hierarchy) Tabs() []*Tab {
	var out []*Tab
	for _, w := range h.Windows {
		out = append(out, w.Tabs...)
	}
	return out
}

// Session finds a session by id, returning nil when the snapshot has no such
// session.
func (h *Hierarchy) Session(id string) *Session {
	for _, s := range h.Sessions() {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Tab finds a tab by id, returning nil when the snapshot has no such tab.
func (h *Hierarchy) Tab(id string) *Tab {
	for _, t := range h.Tabs() {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Window finds a window by id, returning nil when the snapshot has no such
// window.
func (h *Hierarchy) Window(id string) *Window {
	for _, w := range h.Windows {
		if w.ID == id {
			return w
		}
	}
	return nil
}

// ActiveSession returns the tab's focused pane, or nil when iTerm2 did not say
// which it is — which is what an iTerm2 older than protocol 1.18 does.
func (t *Tab) ActiveSession() *Session {
	if t.ActiveSessionID == "" {
		return nil
	}
	for _, s := range t.Sessions {
		if s.ID == t.ActiveSessionID {
			return s
		}
	}
	return nil
}

// newHierarchy converts a ListSessionsResponse into the hand-written types.
//
// Every field is copied rather than referenced, so the result does not alias the
// response and cannot change under the caller.
func newHierarchy(c api, resp *apipb.ListSessionsResponse) *Hierarchy {
	h := &Hierarchy{}
	for _, pw := range resp.GetWindows() {
		w := &Window{
			conn:          c,
			ID:            pw.GetWindowId(),
			Number:        pw.GetNumber(),
			Frame:         newRect(pw.GetFrame()),
			SelectedTabID: pw.GetSelectedTabId(),
		}
		for _, pt := range pw.GetTabs() {
			t := &Tab{
				conn:             c,
				ID:               pt.GetTabId(),
				WindowID:         w.ID,
				ActiveSessionID:  pt.GetActiveSessionId(),
				TmuxWindowID:     pt.GetTmuxWindowId(),
				TmuxConnectionID: pt.GetTmuxConnectionId(),
			}
			// tab_group_id unset means no group; api.proto says the other three
			// fields are meaningful only when it is set, so they are read only
			// then.
			if pt.TabGroupId != nil {
				t.Group = &TabGroup{
					ID:        pt.GetTabGroupId(),
					Name:      pt.GetTabGroupName(),
					Color:     pt.GetTabGroupColor(),
					Collapsed: pt.GetTabGroupCollapsed(),
				}
			}
			t.splitTree = newSplitNode(c, pt.GetRoot())
			t.Sessions = t.splitTree.Panes()
			for _, ps := range pt.GetMinimizedSessions() {
				t.MinimizedSessions = append(t.MinimizedSessions, newSession(c, ps, false))
			}
			w.Tabs = append(w.Tabs, t)
		}
		h.Windows = append(h.Windows, w)
	}
	for _, ps := range resp.GetBuriedSessions() {
		h.BuriedSessions = append(h.BuriedSessions, newSession(c, ps, true))
	}
	return h
}

// newSplitNode converts api.proto's SplitTreeNode, depth first in link order,
// which is left-to-right for a vertical divider and top-to-bottom otherwise.
//
// A link with neither child set is skipped rather than treated as an error: it
// would mean a future iTerm2 added a third kind of child, and dropping one pane
// beats failing the whole call.
func newSplitNode(c api, node *apipb.SplitTreeNode) *SplitNode {
	if node == nil {
		return nil
	}
	n := &SplitNode{Vertical: node.GetVertical()}
	for _, link := range node.GetLinks() {
		switch child := link.GetChild().(type) {
		case *apipb.SplitTreeNode_SplitTreeLink_Session:
			n.Children = append(n.Children, &SplitNode{Session: newSession(c, child.Session, false)})
		case *apipb.SplitTreeNode_SplitTreeLink_Node:
			if sub := newSplitNode(c, child.Node); sub != nil {
				n.Children = append(n.Children, sub)
			}
		}
	}
	return n
}

func newSession(c api, s *apipb.SessionSummary, buried bool) *Session {
	return &Session{
		conn:  c,
		ID:    s.GetUniqueIdentifier(),
		Title: s.GetTitle(),
		Frame: newRect(s.GetFrame()),
		GridSize: Size{
			Width:  s.GetGridSize().GetWidth(),
			Height: s.GetGridSize().GetHeight(),
		},
		Buried: buried,
	}
}

func newRect(f *apipb.Frame) Rect {
	return Rect{
		X:      f.GetOrigin().GetX(),
		Y:      f.GetOrigin().GetY(),
		Width:  f.GetSize().GetWidth(),
		Height: f.GetSize().GetHeight(),
	}
}
