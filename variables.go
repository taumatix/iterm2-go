package iterm2

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// AllVariables is the name that asks for every variable in one JSON object
// instead of a single value, as api.proto's "special value" for the get field.
const AllVariables = "*"

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
	if len(names) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	req, err := variableRequest(scope, identifier)
	if err != nil {
		return nil, err
	}
	req.Get = names

	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_VariableRequest{VariableRequest: req},
	})
	if err != nil {
		return nil, err
	}
	body := resp.GetVariableResponse()
	if err := checkStatus("GetVariables", body.GetStatus()); err != nil {
		return nil, err
	}

	values := body.GetValues()
	// api.proto: values is 1:1 with get. A different length means iTerm2 and this
	// package disagree about the response, and pairing them up anyway would
	// silently attach values to the wrong names.
	if len(values) != len(names) {
		return nil, &APIError{Message: fmt.Sprintf("GetVariables: asked for %d variables, iTerm2 answered with %d values", len(names), len(values))}
	}

	out := make(map[string]json.RawMessage, len(values))
	for i, v := range values {
		// "null" is how iTerm2 spells an unset variable. Leaving it out lets a
		// caller tell "unset" from "set to something" with a map lookup.
		if v == "null" {
			continue
		}
		out[names[i]] = json.RawMessage(v)
	}
	return out, nil
}

// GetStringVariable reads one variable and decodes it as a string.
//
// It returns ok false when the variable is unset. A variable holding something
// other than a string — a number, or the object [AllVariables] returns — is an
// error, because silently rendering it would hide the mismatch.
func (c *Conn) GetStringVariable(ctx context.Context, scope VariableScope, identifier, name string) (value string, ok bool, err error) {
	vars, err := c.GetVariables(ctx, scope, identifier, name)
	if err != nil {
		return "", false, err
	}
	raw, present := vars[name]
	if !present {
		return "", false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("iterm2: variable %q is not a string: %w", name, err)
	}
	return value, true, nil
}

// SetVariables writes variables on one object.
//
// Values must be JSON, so a string needs its quotes. iTerm2 rejects any name
// not beginning with "user." and answers INVALID_NAME, which arrives as a
// [*StatusError]: the built-in variables are iTerm2's to write, not a script's.
func (c *Conn) SetVariables(ctx context.Context, scope VariableScope, identifier string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	req, err := variableRequest(scope, identifier)
	if err != nil {
		return err
	}
	for name, value := range values {
		req.Set = append(req.Set, &apipb.VariableRequest_Set{
			Name:  proto.String(name),
			Value: proto.String(value),
		})
	}

	resp, err := c.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_VariableRequest{VariableRequest: req},
	})
	if err != nil {
		return err
	}
	return checkStatus("SetVariables", resp.GetVariableResponse().GetStatus())
}

// SetStringVariable writes one string variable, doing the JSON quoting.
//
// The name must begin with "user." — see [Conn.SetVariables].
func (c *Conn) SetStringVariable(ctx context.Context, scope VariableScope, identifier, name, value string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("iterm2: encoding variable %q: %w", name, err)
	}
	return c.SetVariables(ctx, scope, identifier, map[string]string{name: string(encoded)})
}

// Variable reads one of this session's variables as a string, such as "jobName",
// "path" or "user.something".
func (s *Session) Variable(ctx context.Context, name string) (string, bool, error) {
	return s.conn.GetStringVariable(ctx, ScopeSession, s.ID, name)
}

// SetVariable writes one of this session's variables. The name must begin with
// "user.".
func (s *Session) SetVariable(ctx context.Context, name, value string) error {
	return s.conn.SetStringVariable(ctx, ScopeSession, s.ID, name, value)
}

// variableRequest builds the request with the scope oneof set, which api.proto
// expresses as one of four differently typed fields rather than a scope enum.
//
// An empty identifier for a session, tab or window scope is refused here: iTerm2
// would answer MISSING_SCOPE for an unset oneof, but an empty string sets the
// oneof to the empty id, which reads as a NOT_FOUND for a session that was never
// named.
func variableRequest(scope VariableScope, identifier string) (*apipb.VariableRequest, error) {
	if !scope.valid() {
		return nil, errUnsetScope
	}
	if scope != ScopeApp && identifier == "" {
		return nil, &APIError{Message: "iterm2: scope " + scope.String() + " needs an identifier"}
	}
	req := &apipb.VariableRequest{}
	switch scope {
	case ScopeSession:
		req.Scope = &apipb.VariableRequest_SessionId{SessionId: identifier}
	case ScopeTab:
		req.Scope = &apipb.VariableRequest_TabId{TabId: identifier}
	case ScopeWindow:
		req.Scope = &apipb.VariableRequest_WindowId{WindowId: identifier}
	case ScopeApp:
		req.Scope = &apipb.VariableRequest_App{App: true}
	}
	return req, nil
}
