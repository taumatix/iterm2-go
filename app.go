package iterm2

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// ListSessions returns a snapshot of every window, tab and session.
func (c api) ListSessions(ctx context.Context) (*Hierarchy, error) {
	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ListSessionsRequest{
			ListSessionsRequest: &apipb.ListSessionsRequest{},
		},
	})
	if err != nil {
		return nil, err
	}
	// ListSessionsResponse carries no status field — the only way it fails is a
	// malformed request, which Do already reports as an *APIError.
	body := resp.GetListSessionsResponse()
	if body == nil {
		return nil, &APIError{Message: "ListSessions: iTerm2 answered without a list_sessions_response"}
	}
	return newHierarchy(c, body), nil
}

// CreateTabOptions configures [Conn.CreateTab].
//
// It is a struct rather than a list of functional options so that a field added
// later is an additive change for callers who construct it with field names.
type CreateTabOptions struct {
	// ProfileName selects the profile. Empty uses the default profile.
	ProfileName string

	// WindowID puts the tab in an existing window. Empty creates a new window.
	WindowID string

	// TabIndex is the position for the new tab, and is valid only with WindowID.
	// nil lets iTerm2 choose. An index iTerm2 cannot honour still creates the
	// tab — it answers INVALID_TAB_INDEX, which arrives as a [*StatusError]
	// even though the tab now exists.
	TabIndex *uint32

	// SelectTab, when pointed at false, creates the tab in the background
	// without selecting it or raising its window. nil keeps iTerm2's default of
	// selecting it.
	SelectTab *bool

	// CustomProfileProperties overrides profile settings for this session only.
	// Each value must be JSON, matching ProfileProperty.json_value in
	// api.proto — a string setting therefore needs its quotes, as in
	// `{"Command": "\"/bin/zsh\""}`.
	CustomProfileProperties map[string]string
}

// NewTab identifies what [Conn.CreateTab] created.
type NewTab struct {
	WindowID string

	// TabID is rendered as a decimal string because that is the form the rest of
	// the API uses: api.proto types CreateTabResponse.tab_id as int32 while
	// ListSessionsResponse.Tab.tab_id is a string. That the two are the same
	// namespace is inferred from api.proto, not yet confirmed against a running
	// iTerm2 — see ROADMAP.md.
	TabID string

	SessionID string
}

// CreateTab opens a new tab, or a new window when opts.WindowID is empty.
func (c api) CreateTab(ctx context.Context, opts CreateTabOptions) (*NewTab, error) {
	if opts.TabIndex != nil && opts.WindowID == "" {
		// api.proto: "Valid to set only if window_id is set." iTerm2 would
		// answer INVALID_TAB_INDEX, having already made the tab; refusing here
		// avoids the half-done outcome.
		return nil, &APIError{Message: "CreateTab: TabIndex needs WindowID"}
	}

	req := &apipb.CreateTabRequest{}
	if opts.ProfileName != "" {
		req.ProfileName = proto.String(opts.ProfileName)
	}
	if opts.WindowID != "" {
		req.WindowId = proto.String(opts.WindowID)
	}
	if opts.TabIndex != nil {
		req.TabIndex = proto.Uint32(*opts.TabIndex)
	}
	if opts.SelectTab != nil {
		req.SelectTab = proto.Bool(*opts.SelectTab)
	}
	req.CustomProfileProperties = profileProperties(opts.CustomProfileProperties)

	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_CreateTabRequest{CreateTabRequest: req},
	})
	if err != nil {
		return nil, err
	}
	body := resp.GetCreateTabResponse()
	if err := checkStatus("CreateTab", body.GetStatus()); err != nil {
		return nil, err
	}
	return &NewTab{
		WindowID:  body.GetWindowId(),
		TabID:     strconv.FormatInt(int64(body.GetTabId()), 10),
		SessionID: body.GetSessionId(),
	}, nil
}

// SendText types text into the session, as though the user had.
//
// The session may be [SessionAll] or [SessionActive]. suppressBroadcast keeps
// the text out of other sessions when the user has broadcasting switched on.
func (c api) SendText(ctx context.Context, sessionID, text string, suppressBroadcast bool) error {
	if sessionID == "" {
		return &APIError{Message: "SendText: no session"}
	}
	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_SendTextRequest{
			SendTextRequest: &apipb.SendTextRequest{
				Session:           proto.String(sessionID),
				Text:              proto.String(text),
				SuppressBroadcast: proto.Bool(suppressBroadcast),
			},
		},
	})
	if err != nil {
		return err
	}
	return checkStatus("SendText", resp.GetSendTextResponse().GetStatus())
}

// SendText types text into this session.
func (s *Session) SendText(ctx context.Context, text string) error {
	return s.conn.SendText(ctx, s.ID, text, false)
}

// SplitDirection is which way a pane divides.
type SplitDirection int32

const (
	// SplitVertical puts the new pane beside the old one, divided by a vertical
	// line. api.proto names the divider, not the arrangement.
	SplitVertical SplitDirection = SplitDirection(apipb.SplitPaneRequest_VERTICAL)

	// SplitHorizontal puts the new pane above or below the old one.
	SplitHorizontal SplitDirection = SplitDirection(apipb.SplitPaneRequest_HORIZONTAL)
)

// SplitPaneOptions configures [Conn.SplitPane].
type SplitPaneOptions struct {
	Direction SplitDirection

	// Before puts the new pane above or to the left of the one being split.
	Before bool

	// ProfileName selects the profile. Empty uses the default profile.
	ProfileName string

	// CustomProfileProperties overrides profile settings for the new session
	// only. Values are JSON; see [CreateTabOptions.CustomProfileProperties].
	CustomProfileProperties map[string]string
}

// SplitPane divides a session and returns the ids of the sessions created.
//
// More than one id comes back when sessionID is [SessionAll]. api.proto warns
// that a tmux integration session reports none, because the split happens only
// if and when the tmux server acts on it.
func (c api) SplitPane(ctx context.Context, sessionID string, opts SplitPaneOptions) ([]string, error) {
	if sessionID == "" {
		return nil, &APIError{Message: "SplitPane: no session"}
	}
	req := &apipb.SplitPaneRequest{
		Session:        proto.String(sessionID),
		SplitDirection: apipb.SplitPaneRequest_SplitDirection(opts.Direction).Enum(),
		Before:         proto.Bool(opts.Before),
	}
	if opts.ProfileName != "" {
		req.ProfileName = proto.String(opts.ProfileName)
	}
	req.CustomProfileProperties = profileProperties(opts.CustomProfileProperties)

	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_SplitPaneRequest{SplitPaneRequest: req},
	})
	if err != nil {
		return nil, err
	}
	body := resp.GetSplitPaneResponse()
	// CANNOT_SPLIT is reported even when some sessions did split, so the ids are
	// returned alongside the error rather than discarded.
	if err := checkStatus("SplitPane", body.GetStatus()); err != nil {
		return body.GetSessionId(), err
	}
	return body.GetSessionId(), nil
}

// SplitPane divides this session.
func (s *Session) SplitPane(ctx context.Context, opts SplitPaneOptions) ([]string, error) {
	return s.conn.SplitPane(ctx, s.ID, opts)
}

// ActivateOptions asks for more than the default of bringing one object forward.
type ActivateOptions struct {
	// OrderWindowFront raises the window containing the target.
	OrderWindowFront bool

	// SelectTab selects the target's tab. Valid for a tab or a session.
	SelectTab bool

	// SelectSession selects the target pane within its tab. Valid for a session.
	SelectSession bool

	// ActivateApp brings iTerm2 itself to the front.
	ActivateApp bool

	// RaiseAllWindows and IgnoringOtherApps apply only with ActivateApp, and
	// correspond to ActivateRequest.App in api.proto.
	RaiseAllWindows   bool
	IgnoringOtherApps bool
}

// Activate brings iTerm2 to the front without selecting anything in particular.
func (c api) Activate(ctx context.Context, opts ActivateOptions) error {
	return c.activate(ctx, &apipb.ActivateRequest{}, opts)
}

// Activate selects this session and, with [ActivateOptions], raises its tab,
// window and application.
func (s *Session) Activate(ctx context.Context, opts ActivateOptions) error {
	return s.conn.activate(ctx, &apipb.ActivateRequest{
		Identifier: &apipb.ActivateRequest_SessionId{SessionId: s.ID},
	}, opts)
}

// Activate selects this tab.
func (t *Tab) Activate(ctx context.Context, opts ActivateOptions) error {
	return t.conn.activate(ctx, &apipb.ActivateRequest{
		Identifier: &apipb.ActivateRequest_TabId{TabId: t.ID},
	}, opts)
}

// Activate brings this window forward.
func (w *Window) Activate(ctx context.Context, opts ActivateOptions) error {
	return w.conn.activate(ctx, &apipb.ActivateRequest{
		Identifier: &apipb.ActivateRequest_WindowId{WindowId: w.ID},
	}, opts)
}

// activate takes a request rather than just the identifier because the oneof
// wrapper interface protoc-gen-go emits is unexported, so it cannot be named
// from outside apipb.
func (c api) activate(ctx context.Context, req *apipb.ActivateRequest, opts ActivateOptions) error {
	if opts.OrderWindowFront {
		req.OrderWindowFront = proto.Bool(true)
	}
	if opts.SelectTab {
		req.SelectTab = proto.Bool(true)
	}
	if opts.SelectSession {
		req.SelectSession = proto.Bool(true)
	}
	if opts.ActivateApp {
		req.ActivateApp = &apipb.ActivateRequest_App{
			RaiseAllWindows:   proto.Bool(opts.RaiseAllWindows),
			IgnoringOtherApps: proto.Bool(opts.IgnoringOtherApps),
		}
	}
	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ActivateRequest{ActivateRequest: req},
	})
	if err != nil {
		return err
	}
	return checkStatus("Activate", resp.GetActivateResponse().GetStatus())
}

// CloseSessions closes panes by id.
//
// force skips the confirmation iTerm2 would otherwise show for a session with a
// running job; without it, a user who declines leaves that session open and the
// call reports USER_DECLINED for it.
//
// iTerm2 answers with one status per target, so closing several can partly
// succeed. The returned error joins one [*StatusError] per target that failed,
// naming it, and is nil only when every one closed.
func (c api) CloseSessions(ctx context.Context, force bool, ids ...string) error {
	return c.close(ctx, &apipb.CloseRequest{
		Target: &apipb.CloseRequest_Sessions{
			Sessions: &apipb.CloseRequest_CloseSessions{SessionIds: ids},
		},
		Force: proto.Bool(force),
	}, "CloseSessions", ids)
}

// CloseTabs closes tabs by id. See [Conn.CloseSessions] for force and the
// per-target statuses.
func (c api) CloseTabs(ctx context.Context, force bool, ids ...string) error {
	return c.close(ctx, &apipb.CloseRequest{
		Target: &apipb.CloseRequest_Tabs{
			Tabs: &apipb.CloseRequest_CloseTabs{TabIds: ids},
		},
		Force: proto.Bool(force),
	}, "CloseTabs", ids)
}

// CloseWindows closes windows by id. See [Conn.CloseSessions] for force and the
// per-target statuses.
func (c api) CloseWindows(ctx context.Context, force bool, ids ...string) error {
	return c.close(ctx, &apipb.CloseRequest{
		Target: &apipb.CloseRequest_Windows{
			Windows: &apipb.CloseRequest_CloseWindows{WindowIds: ids},
		},
		Force: proto.Bool(force),
	}, "CloseWindows", ids)
}

// Close closes this pane.
func (s *Session) Close(ctx context.Context, force bool) error {
	return s.conn.CloseSessions(ctx, force, s.ID)
}

// Close closes this tab and every pane in it.
func (t *Tab) Close(ctx context.Context, force bool) error {
	return t.conn.CloseTabs(ctx, force, t.ID)
}

// Close closes this window.
func (w *Window) Close(ctx context.Context, force bool) error {
	return w.conn.CloseWindows(ctx, force, w.ID)
}

func (c api) close(ctx context.Context, req *apipb.CloseRequest, op string, ids []string) error {
	if len(ids) == 0 {
		return &APIError{Message: op + ": nothing to close"}
	}
	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_CloseRequest{CloseRequest: req},
	})
	if err != nil {
		return err
	}
	statuses := resp.GetCloseResponse().GetStatuses()
	// api.proto gives one status per target with no id attached, so they can only
	// be paired positionally. A different count means that pairing is guesswork,
	// and reporting the wrong id as having failed is worse than saying so.
	if len(statuses) != len(ids) {
		return &APIError{Message: fmt.Sprintf("%s: asked to close %d, iTerm2 answered with %d statuses", op, len(ids), len(statuses))}
	}

	var errs []error
	for i, status := range statuses {
		if err := checkStatus(op+" "+ids[i], status); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// profileProperties converts a map of JSON-valued settings into api.proto's
// repeated ProfileProperty. A nil map yields nil, so the field stays unset.
func profileProperties(m map[string]string) []*apipb.ProfileProperty {
	if len(m) == 0 {
		return nil
	}
	out := make([]*apipb.ProfileProperty, 0, len(m))
	for k, v := range m {
		out = append(out, &apipb.ProfileProperty{
			Key:       proto.String(k),
			JsonValue: proto.String(v),
		})
	}
	return out
}

// errUnsetScope names the one mistake the VariableScope zero value invites.
var errUnsetScope = errors.New("iterm2: VariableScope is unset; api.proto numbers the scopes from 1, so there is no default")
