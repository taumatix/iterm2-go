package iterm2

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// ContextMenuItem is an [RPC] offered as an item in a session's context menu,
// labelled DisplayName. Choosing it calls the Handler; map arguments to
// session variables in Defaults, such as "session.path", to act on the session
// the menu was opened on. iTerm2 does not show the result.
type ContextMenuItem struct {
	RPC

	// DisplayName is the menu item's label.
	DisplayName string

	// Identifier must be unique among context-menu items; api.proto asks for a
	// reverse-DNS name, such as com.example.copy-path.
	Identifier string
}

func (m ContextMenuItem) request() (*apipb.NotificationRequest, error) {
	switch {
	case m.DisplayName == "":
		return nil, &APIError{Message: "RegisterContextMenuItem: DisplayName is required"}
	case m.Identifier == "":
		return nil, &APIError{Message: "RegisterContextMenuItem: Identifier is required"}
	}
	req, err := m.RPC.request()
	if err != nil {
		return nil, err
	}
	reg := req.GetRpcRegistrationRequest()
	reg.Role = apipb.RPCRegistrationRequest_CONTEXT_MENU.Enum()
	reg.RoleSpecificAttributes = &apipb.RPCRegistrationRequest_ContextMenuAttributes_{
		ContextMenuAttributes: &apipb.RPCRegistrationRequest_ContextMenuAttributes{
			DisplayName:      proto.String(m.DisplayName),
			UniqueIdentifier: proto.String(m.Identifier),
		},
	}
	return req, nil
}

// RegisterContextMenuItem adds m to iTerm2's session context menu and answers
// its calls until it is unregistered or the connection ends.
func (c *Conn) RegisterContextMenuItem(ctx context.Context, m ContextMenuItem) (*RPCRegistration, error) {
	req, err := m.request()
	if err != nil {
		return nil, err
	}
	sub, err := c.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(c, m.RPC, sub.C(), sub.Unsubscribe), nil
}

// RegisterContextMenuItem is [Conn.RegisterContextMenuItem] on a Persistent:
// the item is registered again on every new connection.
func (p *Persistent) RegisterContextMenuItem(ctx context.Context, m ContextMenuItem) (*RPCRegistration, error) {
	req, err := m.request()
	if err != nil {
		return nil, err
	}
	sub, err := p.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(p, m.RPC, sub.C(), sub.Unsubscribe), nil
}
