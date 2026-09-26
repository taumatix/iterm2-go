package iterm2

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrClosed is returned by every operation on a [Conn] whose connection has
// gone away, whether because [Conn.Close] was called or because iTerm2 quit.
var ErrClosed = errors.New("iterm2: connection closed")

// ErrUnauthorized wraps a handshake iTerm2 refused. The usual causes are the
// Python API being switched off in Settings > General > Magic, and a cookie
// that has already been used — iTerm2 consumes each one exactly once.
var ErrUnauthorized = errors.New("iterm2: unauthorized")

// ErrNotSupported reports a request iTerm2 understood but declined as
// unavailable, such as a notification type this iTerm2 version does not post.
var ErrNotSupported = errors.New("iterm2: not supported by this iTerm2")

// APIError is iTerm2's report that a request could not be understood. It
// corresponds to the `error` field of ServerOriginatedMessage, which iTerm2
// sets only for a malformed request — never as an ordinary outcome.
type APIError struct {
	Message string
}

func (e *APIError) Error() string { return "iterm2: " + e.Message }

// StatusError reports a well-formed request that iTerm2 declined, carrying the
// status enum it answered with.
//
// Op names the operation as this package spells it, so the error reads in Go
// terms; Status is iTerm2's own enum value name, so it can be looked up in
// api.proto.
type StatusError struct {
	Op     string
	Status string
	Code   int32
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("iterm2: %s failed: %s", e.Op, e.Status)
}

// checkStatus turns a non-zero response status into a [StatusError].
//
// Every response status enum in api.proto spells success as `OK = 0`, with one
// exception: InvokeFunctionResponse.Status starts at TIMEOUT = 1, because that
// response reports success through its `disposition` oneof instead. checkStatus
// must not be used on it, and is not. TestEveryStatusEnumUsesZeroForOK asserts
// the rule over the whole file and names that exception, because this function
// is wrong the moment any other enum joins it.
//
// proto2 leaves an unset optional enum reading as its first value, so an omitted
// status also lands here as OK, which matches how iTerm2's Python library reads
// it.
func checkStatus(op string, status protoreflect.Enum) error {
	num := status.Number()
	if num == 0 {
		return nil
	}
	name := fmt.Sprintf("status %d", num)
	if v := status.Descriptor().Values().ByNumber(num); v != nil {
		name = string(v.Name())
	}
	return &StatusError{Op: op, Status: name, Code: int32(num)}
}
