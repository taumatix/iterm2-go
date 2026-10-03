package iterm2

import (
	"context"
	"encoding/json"
)

// The typed requests, written once over [api] and offered by both *Conn and
// *Persistent. The bodies live in app.go and variables.go.

// ListSessions returns a snapshot of every window, tab and session.
func (c *Conn) ListSessions(ctx context.Context) (*Hierarchy, error) {
	return api{c}.ListSessions(ctx)
}

// CreateTab opens a new tab, or a new window when opts.WindowID is empty.
func (c *Conn) CreateTab(ctx context.Context, opts CreateTabOptions) (*NewTab, error) {
	return api{c}.CreateTab(ctx, opts)
}

// SendText types text into the session, as though the user had.
//
// The session may be [SessionAll] or [SessionActive]. suppressBroadcast keeps
// the text out of other sessions when the user has broadcasting switched on.
func (c *Conn) SendText(ctx context.Context, sessionID, text string, suppressBroadcast bool) error {
	return api{c}.SendText(ctx, sessionID, text, suppressBroadcast)
}

// SplitPane divides a session and returns the ids of the sessions created.
//
// More than one id comes back when sessionID is [SessionAll]. api.proto warns
// that a tmux integration session reports none, because the split happens only
// if and when the tmux server acts on it.
func (c *Conn) SplitPane(ctx context.Context, sessionID string, opts SplitPaneOptions) ([]string, error) {
	return api{c}.SplitPane(ctx, sessionID, opts)
}

// Activate brings iTerm2 to the front without selecting anything in particular.
func (c *Conn) Activate(ctx context.Context, opts ActivateOptions) error {
	return api{c}.Activate(ctx, opts)
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
func (c *Conn) CloseSessions(ctx context.Context, force bool, ids ...string) error {
	return api{c}.CloseSessions(ctx, force, ids...)
}

// CloseTabs closes tabs by id. See [Conn.CloseSessions] for force and the
// per-target statuses.
func (c *Conn) CloseTabs(ctx context.Context, force bool, ids ...string) error {
	return api{c}.CloseTabs(ctx, force, ids...)
}

// CloseWindows closes windows by id. See [Conn.CloseSessions] for force and the
// per-target statuses.
func (c *Conn) CloseWindows(ctx context.Context, force bool, ids ...string) error {
	return api{c}.CloseWindows(ctx, force, ids...)
}

// GetVariables reads variables from one object.
//
// identifier names the session, tab or window, and is ignored for [ScopeApp].
// The returned values are JSON, exactly as iTerm2 sends them: a string variable
// arrives quoted. Use [Conn.GetStringVariable] when a string is what is wanted.
//
// The result is keyed by the requested name and omits any variable iTerm2
// reported as unset, which it does by sending JSON null.
//
// iTerm2 refuses to read more than one variable at once from [SessionAll] and
// answers MULTI_GET_DISALLOWED.
func (c *Conn) GetVariables(ctx context.Context, scope VariableScope, identifier string, names ...string) (map[string]json.RawMessage, error) {
	return api{c}.GetVariables(ctx, scope, identifier, names...)
}

// GetStringVariable reads one variable and decodes it as a string.
//
// It returns ok false when the variable is unset. A variable holding something
// other than a string — a number, or the object [AllVariables] returns — is an
// error, because silently rendering it would hide the mismatch.
func (c *Conn) GetStringVariable(ctx context.Context, scope VariableScope, identifier, name string) (value string, ok bool, err error) {
	return api{c}.GetStringVariable(ctx, scope, identifier, name)
}

// SetVariables writes variables on one object.
//
// Values must be JSON, so a string needs its quotes. iTerm2 rejects any name
// not beginning with "user." and answers INVALID_NAME, which arrives as a
// [*StatusError]: the built-in variables are iTerm2's to write, not a script's.
func (c *Conn) SetVariables(ctx context.Context, scope VariableScope, identifier string, values map[string]string) error {
	return api{c}.SetVariables(ctx, scope, identifier, values)
}

// SetStringVariable writes one string variable, doing the JSON quoting.
//
// The name must begin with "user." — see [Conn.SetVariables].
func (c *Conn) SetStringVariable(ctx context.Context, scope VariableScope, identifier, name, value string) error {
	return api{c}.SetStringVariable(ctx, scope, identifier, name, value)
}

// ListSessions is [Conn.ListSessions] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) ListSessions(ctx context.Context) (*Hierarchy, error) {
	return api{p}.ListSessions(ctx)
}

// CreateTab is [Conn.CreateTab] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) CreateTab(ctx context.Context, opts CreateTabOptions) (*NewTab, error) {
	return api{p}.CreateTab(ctx, opts)
}

// SendText is [Conn.SendText] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) SendText(ctx context.Context, sessionID, text string, suppressBroadcast bool) error {
	return api{p}.SendText(ctx, sessionID, text, suppressBroadcast)
}

// SplitPane is [Conn.SplitPane] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) SplitPane(ctx context.Context, sessionID string, opts SplitPaneOptions) ([]string, error) {
	return api{p}.SplitPane(ctx, sessionID, opts)
}

// Activate is [Conn.Activate] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) Activate(ctx context.Context, opts ActivateOptions) error {
	return api{p}.Activate(ctx, opts)
}

// CloseSessions is [Conn.CloseSessions] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) CloseSessions(ctx context.Context, force bool, ids ...string) error {
	return api{p}.CloseSessions(ctx, force, ids...)
}

// CloseTabs is [Conn.CloseTabs] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) CloseTabs(ctx context.Context, force bool, ids ...string) error {
	return api{p}.CloseTabs(ctx, force, ids...)
}

// CloseWindows is [Conn.CloseWindows] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) CloseWindows(ctx context.Context, force bool, ids ...string) error {
	return api{p}.CloseWindows(ctx, force, ids...)
}

// GetVariables is [Conn.GetVariables] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) GetVariables(ctx context.Context, scope VariableScope, identifier string, names ...string) (map[string]json.RawMessage, error) {
	return api{p}.GetVariables(ctx, scope, identifier, names...)
}

// GetStringVariable is [Conn.GetStringVariable] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) GetStringVariable(ctx context.Context, scope VariableScope, identifier, name string) (value string, ok bool, err error) {
	return api{p}.GetStringVariable(ctx, scope, identifier, name)
}

// SetVariables is [Conn.SetVariables] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) SetVariables(ctx context.Context, scope VariableScope, identifier string, values map[string]string) error {
	return api{p}.SetVariables(ctx, scope, identifier, values)
}

// SetStringVariable is [Conn.SetStringVariable] on whichever connection is current. It returns
// [ErrReconnecting] while iTerm2 is away; see [Persistent].
func (p *Persistent) SetStringVariable(ctx context.Context, scope VariableScope, identifier, name, value string) error {
	return api{p}.SetStringVariable(ctx, scope, identifier, name, value)
}
