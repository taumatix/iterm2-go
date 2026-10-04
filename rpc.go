package iterm2

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// RPC is a function iTerm2 can call in this program: from a key binding, a
// trigger, a toolbelt panel's page, or anything else that invokes a script
// function by name, such as `greet(who: "you")`.
//
// This registers it in iTerm2's generic role. The status-bar, session-title
// and context-menu roles take attributes of their own and are not covered yet.
type RPC struct {
	// Name is what iTerm2 calls it by.
	Name string

	// Arguments are the argument names, which with Name make the signature
	// iTerm2 matches a call against.
	Arguments []string

	// Defaults fills an argument the caller left out from an iTerm2 variable,
	// by path: {"session_id": "session.id"}. Each key must be in Arguments.
	Defaults map[string]string

	// Timeout is how long iTerm2 waits for an answer. Zero leaves it to iTerm2.
	Timeout time.Duration

	// Handler answers one call. Each argument arrives as the JSON iTerm2 sent;
	// the result is sent back as JSON, and an error as an exception iTerm2 can
	// show. ctx ends when the registration does.
	Handler func(ctx context.Context, args map[string]json.RawMessage) (any, error)
}

func (r RPC) request() (*apipb.NotificationRequest, error) {
	switch {
	case r.Name == "":
		return nil, &APIError{Message: "RegisterRPC: Name is required"}
	case r.Handler == nil:
		return nil, &APIError{Message: "RegisterRPC: Handler is required"}
	}
	reg := &apipb.RPCRegistrationRequest{
		Name: proto.String(r.Name),
		Role: apipb.RPCRegistrationRequest_GENERIC.Enum(),
	}
	known := make(map[string]bool, len(r.Arguments))
	for _, a := range r.Arguments {
		known[a] = true
		reg.Arguments = append(reg.Arguments, &apipb.RPCRegistrationRequest_RPCArgumentSignature{Name: proto.String(a)})
	}
	for _, a := range r.Arguments {
		if path, ok := r.Defaults[a]; ok {
			reg.Defaults = append(reg.Defaults, &apipb.RPCRegistrationRequest_RPCArgument{
				Name: proto.String(a), Path: proto.String(path),
			})
		}
	}
	for a := range r.Defaults {
		if !known[a] {
			return nil, &APIError{Message: fmt.Sprintf("RegisterRPC: default for %q, which is not among Arguments", a)}
		}
	}
	if r.Timeout > 0 {
		reg.Timeout = proto.Float32(float32(r.Timeout.Seconds()))
	}
	return &apipb.NotificationRequest{
		NotificationType: apipb.NotificationType_NOTIFY_ON_SERVER_ORIGINATED_RPC.Enum(),
		Arguments:        &apipb.NotificationRequest_RpcRegistrationRequest{RpcRegistrationRequest: reg},
	}, nil
}

// RPCRegistration is a registered [RPC]. Unregister it to stop answering.
type RPCRegistration struct {
	unsubscribe func(context.Context) error
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// Unregister stops the function. Calls already running are cancelled and
// waited for.
func (r *RPCRegistration) Unregister(ctx context.Context) error {
	err := r.unsubscribe(ctx)
	r.cancel()
	r.wg.Wait()
	return err
}

// RegisterRPC registers fn and answers iTerm2's calls to it until it is
// unregistered or the connection ends.
func (c *Conn) RegisterRPC(ctx context.Context, fn RPC) (*RPCRegistration, error) {
	req, err := fn.request()
	if err != nil {
		return nil, err
	}
	sub, err := c.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(c, fn, sub.C(), sub.Unsubscribe), nil
}

// RegisterRPC is [Conn.RegisterRPC] on a Persistent: the registration is a
// [DurableSubscription], so the function is registered again on every new
// connection and answers on whichever connection is current.
func (p *Persistent) RegisterRPC(ctx context.Context, fn RPC) (*RPCRegistration, error) {
	req, err := fn.request()
	if err != nil {
		return nil, err
	}
	sub, err := p.Subscribe(ctx, req)
	if err != nil {
		return nil, err
	}
	return serveRPC(p, fn, sub.C(), sub.Unsubscribe), nil
}

// serveRPC answers the calls arriving on calls. Every RPC subscription on a
// connection hears every call, so each answers only those naming it.
func serveRPC(d doer, fn RPC, calls <-chan *apipb.Notification, unsubscribe func(context.Context) error) *RPCRegistration {
	ctx, cancel := context.WithCancel(context.Background())
	r := &RPCRegistration{unsubscribe: unsubscribe, cancel: cancel}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for n := range calls {
			note := n.GetServerOriginatedRpcNotification()
			if note.GetRpc().GetName() != fn.Name {
				continue
			}
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				answerRPC(ctx, d, fn, note)
			}()
		}
	}()
	return r
}

func answerRPC(ctx context.Context, d doer, fn RPC, note *apipb.ServerOriginatedRPCNotification) {
	args := make(map[string]json.RawMessage, len(note.GetRpc().GetArguments()))
	for _, a := range note.GetRpc().GetArguments() {
		args[a.GetName()] = json.RawMessage(a.GetJsonValue())
	}

	result := &apipb.ServerOriginatedRPCResultRequest{RequestId: proto.String(note.GetRequestId())}
	value, err := fn.Handler(ctx, args)
	var body []byte
	if err == nil {
		body, err = json.Marshal(value)
	}
	if err != nil {
		// api.proto: "Exceptions should be dictionaries with a key of
		// "reason" having a string value describing what went wrong."
		exception, _ := json.Marshal(map[string]string{"reason": err.Error()})
		result.Result = &apipb.ServerOriginatedRPCResultRequest_JsonException{JsonException: string(exception)}
	} else {
		result.Result = &apipb.ServerOriginatedRPCResultRequest_JsonValue{JsonValue: string(body)}
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), resubscribeTimeout)
	defer cancel()
	// There is nobody to report a failed send to: iTerm2 times the call out.
	_, _ = d.Do(sendCtx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ServerOriginatedRpcResultRequest{ServerOriginatedRpcResultRequest: result},
	})
}
