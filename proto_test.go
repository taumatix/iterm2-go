package iterm2_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/taumatix/iterm2-go/apipb"
)

// apiProto returns the descriptor for iTerm2's api.proto as this module
// generated it.
func apiProto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	fd, err := protoregistry.GlobalFiles.FindFileByPath("api.proto")
	require.NoError(t, err, "api.proto is not in the registry; did apipb stop being imported?")
	return fd
}

// statusEnumsWithoutOK are the Status enums in api.proto that do not number
// success as 0, with the reason each is exempt. checkStatus must never be used
// on one of them.
//
// InvokeFunctionResponse reports success through its `disposition` oneof — a
// Success submessage — so its Status enum lists only failures and starts at
// TIMEOUT = 1. This package does not implement InvokeFunction, so nothing calls
// checkStatus on it; the exemption is recorded rather than removed so that a
// second enum of this shape appearing upstream fails the test instead of
// quietly joining it.
var statusEnumsWithoutOK = map[protoreflect.FullName]string{
	"iterm2.InvokeFunctionResponse.Status": "success is the disposition oneof, not a status of 0",
}

func TestEveryStatusEnumUsesZeroForOK(t *testing.T) {
	// checkStatus treats 0 as success, which is only sound while api.proto spells
	// success that way. proto2 also reads an unset optional enum as its first
	// value, so an omitted status lands on OK — matching how iTerm2's Python
	// library reads it.
	//
	// This is the assertion errors.go's comment points at. A status enum numbered
	// differently fails here rather than letting a caller see a failure as
	// success.
	var checked int
	seenExempt := map[protoreflect.FullName]bool{}

	forEachEnum(apiProto(t).Messages(), func(enum protoreflect.EnumDescriptor) {
		if enum.Name() != "Status" {
			return
		}
		if _, exempt := statusEnumsWithoutOK[enum.FullName()]; exempt {
			seenExempt[enum.FullName()] = true
			return
		}
		checked++
		zero := enum.Values().ByNumber(0)
		if assert.NotNil(t, zero, "%s has no value numbered 0", enum.FullName()) {
			assert.Equal(t, protoreflect.Name("OK"), zero.Name(),
				"%s numbers %s as 0, not OK", enum.FullName(), zero.Name())
		}
	})
	assert.Greater(t, checked, 20, "far fewer status enums than api.proto has; the walk is wrong")

	// An exemption for an enum that no longer exists is stale, and would hide the
	// next one.
	for name := range statusEnumsWithoutOK {
		assert.True(t, seenExempt[name], "%s is exempted but no longer in api.proto", name)
	}
}

func TestNotificationTypeHasNoZeroValueToMistakeForUnset(t *testing.T) {
	// Subscribe refuses a type it does not recognise. Were there a type numbered
	// 0, a caller who forgot to set one would silently subscribe to it.
	enum := apiProto(t).Enums().ByName("NotificationType")
	require.NotNil(t, enum)
	assert.Nil(t, enum.Values().ByNumber(0),
		"NotificationType gained a zero value, which an unset field is indistinguishable from")
}

func TestSubscribeKnowsEveryNotificationTypeApiProtoDefines(t *testing.T) {
	// A type this package does not recognise is refused by Subscribe rather than
	// silently delivering nothing, so an upstream addition shows up here as a
	// missing case instead of as a channel that never produces anything.
	enum := apiProto(t).Enums().ByName("NotificationType")
	require.NotNil(t, enum)

	var unhandled []string
	for i := range enum.Values().Len() {
		value := enum.Values().Get(i)
		kind := apipb.NotificationType(value.Number())
		// KEYSTROKE_FILTER posts nothing, so Subscribe rejects it by design.
		if kind == apipb.NotificationType_KEYSTROKE_FILTER {
			continue
		}
		if !subscribable(t, kind) {
			unhandled = append(unhandled, string(value.Name()))
		}
	}
	assert.Empty(t, unhandled,
		"api.proto defines notification types this package does not handle; add them to Subscription.matches")
}

// subscribable reports whether Subscribe accepts kind, measured by sending a
// real subscription to a fake iTerm2 that accepts everything.
func subscribable(t *testing.T, kind apipb.NotificationType) bool {
	t.Helper()
	conn := connect(t, startFake(t, acceptSubscriptions()))
	_, err := conn.Subscribe(testContext(t), &apipb.NotificationRequest{
		NotificationType: kind.Enum(),
	})
	return err == nil
}

// forEachEnum walks every enum nested anywhere in messages.
func forEachEnum(messages protoreflect.MessageDescriptors, fn func(protoreflect.EnumDescriptor)) {
	for i := range messages.Len() {
		msg := messages.Get(i)
		enums := msg.Enums()
		for j := range enums.Len() {
			fn(enums.Get(j))
		}
		forEachEnum(msg.Messages(), fn)
	}
}
