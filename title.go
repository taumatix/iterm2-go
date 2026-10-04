package iterm2

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// TitleProvider is an [RPC] iTerm2 calls to compute a session's title. Once
// registered it appears among the title choices in a profile's settings, under
// DisplayName, and a session using it shows whatever its Handler returns.
//
// Defaults is how a title follows the session: map an argument to a variable
// such as "session.path" or "user.gitBranch", and iTerm2 calls the provider
// again when that variable changes.
type TitleProvider struct {
	RPC

	// DisplayName is how iTerm2 lists the provider in its settings.
	DisplayName string

	// Identifier must be unique among title providers; api.proto asks for a
	// reverse-DNS name, such as com.example.branch-title.
	Identifier string
}

func (t TitleProvider) request() (*apipb.NotificationRequest, error) {
	switch {
	case t.DisplayName == "":
		return nil, &APIError{Message: "RegisterTitleProvider: DisplayName is required"}
	case t.Identifier == "":
		return nil, &APIError{Message: "RegisterTitleProvider: Identifier is required"}
	}
	req, err := t.RPC.request()
	if err != nil {
		return nil, err
	}
	reg := req.GetRpcRegistrationRequest()
	reg.Role = apipb.RPCRegistrationRequest_SESSION_TITLE.Enum()
	reg.RoleSpecificAttributes = &apipb.RPCRegistrationRequest_SessionTitleAttributes_{
		SessionTitleAttributes: &apipb.RPCRegistrationRequest_SessionTitleAttributes{
			DisplayName:      proto.String(t.DisplayName),
			UniqueIdentifier: proto.String(t.Identifier),
		},
	}
	return req, nil
}

// fn is the provider's RPC with its Handler held to returning a string, which
// iTerm2 shows as the title.
func (t TitleProvider) fn() RPC {
	fn := t.RPC
	handler := t.Handler
	if handler == nil {
		return fn
	}
	fn.Handler = func(ctx context.Context, args map[string]json.RawMessage) (any, error) {
		v, err := handler(ctx, args)
		if err != nil {
			return nil, err
		}
		if _, ok := v.(string); !ok {
			return nil, fmt.Errorf("title provider %q returned %T; a title must be a string", t.Name, v)
		}
		return v, nil
	}
	return fn
}

// RegisterTitleProvider registers t in iTerm2's session-title role and answers
// its calls until it is unregistered or the connection ends.
func (c *Conn) RegisterTitleProvider(ctx context.Context, t TitleProvider) (*RPCRegistration, error) {
	req, err := t.request()
	if err != nil {
		return nil, err
	}
	sub, err := c.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(c, t.fn(), sub.C(), sub.Unsubscribe), nil
}

// RegisterTitleProvider is [Conn.RegisterTitleProvider] on a Persistent: it is
// registered again on every new connection.
func (p *Persistent) RegisterTitleProvider(ctx context.Context, t TitleProvider) (*RPCRegistration, error) {
	req, err := t.request()
	if err != nil {
		return nil, err
	}
	sub, err := p.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(p, t.fn(), sub.C(), sub.Unsubscribe), nil
}
