package iterm2

import (
	"context"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// Tool is a panel in iTerm2's toolbelt, showing a web page.
type Tool struct {
	// Name is shown to the user.
	Name string

	// Identifier must be unique to the tool. api.proto asks for a reverse-DNS
	// name, such as com.example.mytool. Registering the same identifier again
	// replaces the tool.
	Identifier string

	// URL is the page the panel loads.
	URL string

	// Reveal shows the tool even if it was registered before. iTerm2 adds a
	// tool to the visible set by itself only the first time; it does not open
	// a hidden toolbelt either way.
	Reveal bool
}

func (t Tool) request(reveal bool) (*apipb.ClientOriginatedMessage, error) {
	switch {
	case t.Name == "":
		return nil, &APIError{Message: "RegisterTool: Name is required"}
	case t.Identifier == "":
		return nil, &APIError{Message: "RegisterTool: Identifier is required"}
	case t.URL == "":
		return nil, &APIError{Message: "RegisterTool: URL is required"}
	}
	return &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_RegisterToolRequest{
			RegisterToolRequest: &apipb.RegisterToolRequest{
				Name:                      proto.String(t.Name),
				Identifier:                proto.String(t.Identifier),
				ToolType:                  apipb.RegisterToolRequest_WEB_VIEW_TOOL.Enum(),
				URL:                       proto.String(t.URL),
				RevealIfAlreadyRegistered: proto.Bool(reveal),
			},
		},
	}, nil
}

func registerTool(ctx context.Context, d doer, t Tool, reveal bool) error {
	req, err := t.request(reveal)
	if err != nil {
		return err
	}
	resp, err := d.Do(ctx, req)
	if err != nil {
		return err
	}
	return checkStatus("RegisterTool", resp.GetRegisterToolResponse().GetStatus())
}

// RegisterTool adds a web-view panel to iTerm2's toolbelt.
//
// iTerm2 forgets the tool when it quits. [Persistent.RegisterTool] registers
// it again on every new connection.
func (c *Conn) RegisterTool(ctx context.Context, t Tool) error {
	return registerTool(ctx, c, t, t.Reveal)
}

// RegisterTool is [Conn.RegisterTool], and the tool is registered again on
// every new connection, before the [Reconnect] is announced. iTerm2 forgets a
// tool when it quits, so without this a panel vanishes at every restart.
//
// Re-registration does not reveal the tool, whatever Reveal says: a restart of
// iTerm2 should not pop the toolbelt open. A tool iTerm2 refuses then is
// reported in [Reconnect.ToolErr]. Registering a tool with the same Identifier
// again replaces it, here as in iTerm2.
//
// Like every call, it returns [ErrReconnecting] while iTerm2 is away, and the
// tool is then not remembered.
func (p *Persistent) RegisterTool(ctx context.Context, t Tool) error {
	// Held across the call, as Subscribe holds it, so a reconnect cannot run
	// between registering on the old connection and recording the tool.
	p.resub.Lock()
	defer p.resub.Unlock()
	if err := registerTool(ctx, p, t, t.Reveal); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tools == nil {
		p.tools = make(map[string]Tool)
	}
	p.tools[t.Identifier] = t
	return nil
}

// reregisterTools runs on a reconnect, with resub held.
func (p *Persistent) reregisterTools(ctx context.Context, conn *Conn) error {
	p.mu.Lock()
	tools := make([]Tool, 0, len(p.tools))
	for _, t := range p.tools {
		tools = append(tools, t)
	}
	p.mu.Unlock()

	var errs []error
	for _, t := range tools {
		rctx, cancel := context.WithTimeout(ctx, resubscribeTimeout)
		if err := registerTool(rctx, conn, t, false); err != nil {
			errs = append(errs, err)
		}
		cancel()
	}
	return errors.Join(errs...)
}
