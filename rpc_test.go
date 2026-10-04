package iterm2_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"github.com/taumatix/iterm2-go/internal/fakeiterm"
)

// rpcHost plays iTerm2's side of a registered function: it records each
// registration and each result the program sends back.
type rpcHost struct {
	mu            sync.Mutex
	registrations []*apipb.RPCRegistrationRequest
	results       chan *apipb.ServerOriginatedRPCResultRequest
}

func newRPCHost() *rpcHost {
	return &rpcHost{results: make(chan *apipb.ServerOriginatedRPCResultRequest, 16)}
}

func (h *rpcHost) option() fakeiterm.Option {
	return fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		if r := req.GetServerOriginatedRpcResultRequest(); r != nil {
			h.results <- r
			return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_ServerOriginatedRpcResultResponse{
				ServerOriginatedRpcResultResponse: &apipb.ServerOriginatedRPCResultResponse{},
			}}
		}
		if nr := req.GetNotificationRequest(); nr != nil && nr.GetSubscribe() {
			if reg := nr.GetRpcRegistrationRequest(); reg != nil {
				h.mu.Lock()
				h.registrations = append(h.registrations, reg)
				h.mu.Unlock()
			}
		}
		return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{Status: apipb.NotificationResponse_OK.Enum()},
		}}
	})
}

func (h *rpcHost) registered() []*apipb.RPCRegistrationRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*apipb.RPCRegistrationRequest(nil), h.registrations...)
}

func (h *rpcHost) result(t *testing.T) *apipb.ServerOriginatedRPCResultRequest {
	t.Helper()
	select {
	case r := <-h.results:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no result came back")
		return nil
	}
}

// call is what iTerm2 sends when the function is invoked.
func call(id, name string, args map[string]string) *apipb.Notification {
	rpc := &apipb.ServerOriginatedRPC{Name: proto.String(name)}
	for k, v := range args {
		rpc.Arguments = append(rpc.Arguments, &apipb.ServerOriginatedRPC_RPCArgument{Name: proto.String(k), JsonValue: proto.String(v)})
	}
	return &apipb.Notification{ServerOriginatedRpcNotification: &apipb.ServerOriginatedRPCNotification{
		RequestId: proto.String(id), Rpc: rpc,
	}}
}

var greet = iterm2.RPC{
	Name:      "greet",
	Arguments: []string{"who", "session_id"},
	Defaults:  map[string]string{"session_id": "session.id"},
	Timeout:   5 * time.Second,
	Handler: func(_ context.Context, args map[string]json.RawMessage) (any, error) {
		var who string
		if err := json.Unmarshal(args["who"], &who); err != nil {
			return nil, err
		}
		return map[string]string{"greeting": "hello " + who}, nil
	},
}

func TestRegisterRPCSendsTheSignatureITerm2MatchesOn(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))

	reg, err := conn.RegisterRPC(testContext(t), greet)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	got := host.registered()
	require.Len(t, got, 1)
	assert.Equal(t, "greet", got[0].GetName())
	require.Len(t, got[0].GetArguments(), 2)
	assert.Equal(t, "who", got[0].GetArguments()[0].GetName())
	assert.Equal(t, "session_id", got[0].GetArguments()[1].GetName())
	require.Len(t, got[0].GetDefaults(), 1)
	assert.Equal(t, "session_id", got[0].GetDefaults()[0].GetName())
	assert.Equal(t, "session.id", got[0].GetDefaults()[0].GetPath())
	assert.InDelta(t, 5.0, got[0].GetTimeout(), 0.001)
	assert.Equal(t, apipb.RPCRegistrationRequest_GENERIC, got[0].GetRole())
}

func TestARegisteredFunctionAnswersACall(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	reg, err := conn.RegisterRPC(testContext(t), greet)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	require.NoError(t, srv.Notify(testContext(t), call("req-1", "greet", map[string]string{"who": `"iTerm2"`, "session_id": `"w0t0p0"`})))

	r := host.result(t)
	assert.Equal(t, "req-1", r.GetRequestId())
	assert.JSONEq(t, `{"greeting":"hello iTerm2"}`, r.GetJsonValue())
	assert.Empty(t, r.GetJsonException())
}

// A handler's error goes back as the exception api.proto asks for, a
// dictionary with a "reason", so iTerm2 can show it.
func TestAFunctionsErrorGoesBackAsAnException(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	failing := greet
	failing.Name = "fail"
	failing.Handler = func(context.Context, map[string]json.RawMessage) (any, error) {
		return nil, errors.New("no such thing")
	}
	reg, err := conn.RegisterRPC(testContext(t), failing)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	require.NoError(t, srv.Notify(testContext(t), call("req-2", "fail", nil)))

	r := host.result(t)
	assert.Equal(t, "req-2", r.GetRequestId())
	assert.JSONEq(t, `{"reason":"no such thing"}`, r.GetJsonException())
}

// Every RPC subscription hears every call. Two functions on one connection
// must each answer only their own.
func TestEachFunctionAnswersOnlyItsOwnCalls(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	conn := connect(t, srv)
	other := greet
	other.Name = "other"
	other.Handler = func(context.Context, map[string]json.RawMessage) (any, error) { return "other", nil }
	for _, rpc := range []iterm2.RPC{greet, other} {
		reg, err := conn.RegisterRPC(testContext(t), rpc)
		require.NoError(t, err)
		defer reg.Unregister(testContext(t))
	}

	require.NoError(t, srv.Notify(testContext(t), call("req-3", "other", nil)))
	r := host.result(t)
	assert.JSONEq(t, `"other"`, r.GetJsonValue())
	select {
	case extra := <-host.results:
		t.Fatalf("a second function answered the same call: %v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRegisterRPCRefusesAnIncompleteFunction(t *testing.T) {
	host := newRPCHost()
	conn := connect(t, startFake(t, host.option()))
	noName := greet
	noName.Name = ""
	noHandler := greet
	noHandler.Handler = nil
	strayDefault := greet
	strayDefault.Defaults = map[string]string{"nope": "session.id"}

	for _, rpc := range []iterm2.RPC{noName, noHandler, strayDefault} {
		_, err := conn.RegisterRPC(testContext(t), rpc)
		var api *iterm2.APIError
		assert.ErrorAs(t, err, &api)
	}
	assert.Empty(t, host.registered())
}

// On a Persistent the registration is a durable subscription, so the
// function is registered again on every new connection and keeps answering.
func TestAPersistentFunctionKeepsAnsweringAcrossAReconnect(t *testing.T) {
	host := newRPCHost()
	srv := startFake(t, host.option())
	p := connectPersistent(t, srv)
	reg, err := p.RegisterRPC(testContext(t), greet)
	require.NoError(t, err)
	defer reg.Unregister(testContext(t))

	srv.CloseConnections()
	nextReconnect(t, p)
	require.Len(t, host.registered(), 2, "the function was not registered again on the new connection")

	require.NoError(t, srv.Notify(testContext(t), call("req-4", "greet", map[string]string{"who": `"again"`})))
	r := host.result(t)
	assert.JSONEq(t, `{"greeting":"hello again"}`, r.GetJsonValue())
}

// iTerm2 identifies what to stop by the request itself, as the Python library
// sends it: the same NotificationRequest with subscribe set to false. An
// unsubscribe that dropped the registration or the variable named nothing.
func TestAnUnsubscribeNamesWhatItStops(t *testing.T) {
	var mu sync.Mutex
	var unsubscribes []*apipb.NotificationRequest
	srv := startFake(t, fakeiterm.WithHandler(func(req *apipb.ClientOriginatedMessage) *apipb.ServerOriginatedMessage {
		if nr := req.GetNotificationRequest(); nr != nil && !nr.GetSubscribe() {
			mu.Lock()
			unsubscribes = append(unsubscribes, nr)
			mu.Unlock()
		}
		return &apipb.ServerOriginatedMessage{Submessage: &apipb.ServerOriginatedMessage_NotificationResponse{
			NotificationResponse: &apipb.NotificationResponse{Status: apipb.NotificationResponse_OK.Enum()},
		}}
	}))
	conn := connect(t, srv)

	reg, err := conn.RegisterRPC(testContext(t), greet)
	require.NoError(t, err)
	require.NoError(t, reg.Unregister(testContext(t)))

	vars, err := conn.SubscribeVariableChanges(testContext(t), iterm2.ScopeSession, "s-1", "jobName")
	require.NoError(t, err)
	require.NoError(t, vars.Unsubscribe(testContext(t)))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, unsubscribes, 2)
	assert.Equal(t, "greet", unsubscribes[0].GetRpcRegistrationRequest().GetName(), "the RPC unsubscribe named no function")
	assert.Equal(t, "jobName", unsubscribes[1].GetVariableMonitorRequest().GetName(), "the variable unsubscribe named no variable")
}
