package iterm2

import (
	"context"
	"encoding/json"

	"github.com/taumatix/iterm2-go/apipb"
)

// Client is the request surface [*Conn] and [*Persistent] share, so code can
// be written once and handed either. Subscriptions are not part of it: a
// Persistent's outlive a reconnect and are a different type.
type Client interface {
	Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error)

	ListSessions(ctx context.Context) (*Hierarchy, error)
	CreateTab(ctx context.Context, opts CreateTabOptions) (*NewTab, error)
	SendText(ctx context.Context, sessionID, text string, suppressBroadcast bool) error
	SplitPane(ctx context.Context, sessionID string, opts SplitPaneOptions) ([]string, error)
	Activate(ctx context.Context, opts ActivateOptions) error
	CloseSessions(ctx context.Context, force bool, ids ...string) error
	CloseTabs(ctx context.Context, force bool, ids ...string) error
	CloseWindows(ctx context.Context, force bool, ids ...string) error

	GetVariables(ctx context.Context, scope VariableScope, identifier string, names ...string) (map[string]json.RawMessage, error)
	GetStringVariable(ctx context.Context, scope VariableScope, identifier, name string) (value string, ok bool, err error)
	SetVariables(ctx context.Context, scope VariableScope, identifier string, values map[string]string) error
	SetStringVariable(ctx context.Context, scope VariableScope, identifier, name, value string) error
}

var (
	_ Client = (*Conn)(nil)
	_ Client = (*Persistent)(nil)
)

// doer sends one request: a *Conn, or a *Persistent on its current connection.
type doer interface {
	Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error)
}

// api carries the typed requests over a doer. The [Window], [Tab] and
// [Session] values a ListSessions returns keep the api that listed them, so
// ones listed through a Persistent follow it across reconnects instead of
// holding the connection that has since gone.
type api struct{ doer }
