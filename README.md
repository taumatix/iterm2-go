# iterm2-go

A Go client for [iTerm2](https://iterm2.com)'s API — the same API its
[Python scripting library](https://iterm2.com/python-api/) uses, spoken directly over the
WebSocket. No Python, no bundled runtime, no script installed into iTerm2's scripts folder.

[![CI](https://github.com/taumatix/iterm2-go/actions/workflows/ci.yml/badge.svg)](https://github.com/taumatix/iterm2-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/taumatix/iterm2-go.svg)](https://pkg.go.dev/github.com/taumatix/iterm2-go)

> **Read [UPSTREAM.md](UPSTREAM.md) before depending on this.** The protocol definition is
> pinned and verified byte-identical to iTerm2's own. The *connection* to a real iTerm2 is
> **not yet verified by any automated run** — every test drives a fake that implements the
> protocol as read from iTerm2's source. See [what is and is not verified](UPSTREAM.md#what-is-and-is-not-verified).

## Install

```sh
go get github.com/taumatix/iterm2-go@v0.2.0
```

Requires Go 1.27 or newer, macOS, and iTerm2 with the API enabled in
**Settings → General → Magic → Enable Python API**. The setting's name says Python; it governs
the socket, not the language.

## Use

```go
package main

import (
	"context"
	"fmt"
	"log"

	iterm2 "github.com/taumatix/iterm2-go"
)

func main() {
	ctx := context.Background()

	conn, err := iterm2.Connect(ctx, iterm2.WithAdvisoryName("my-tool"))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	h, err := conn.ListSessions(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range h.Sessions() {
		fmt.Printf("%s\t%s\n", s.ID, s.Title)
	}

	tab, err := conn.CreateTab(ctx, iterm2.CreateTabOptions{})
	if err != nil {
		log.Fatal(err)
	}
	if err := conn.SendText(ctx, tab.SessionID, "echo hello\n", false); err != nil {
		log.Fatal(err)
	}
}
```

The first connection raises a permission prompt in iTerm2, because the library asks the running
application for a cookie over AppleScript. A program iTerm2 launched itself is handed
`ITERM2_COOKIE` and `ITERM2_KEY` in its environment and is not prompted.

### Watching for changes

```go
sub, err := conn.SubscribeNewSessions(ctx)
if err != nil {
	return err
}
defer sub.Unsubscribe(ctx)

for n := range sub.C() {
	fmt.Println("new session:", n.GetNewSessionNotification().GetSessionId())
}
```

Notifications are dropped rather than blocking when a consumer falls behind — the read loop
delivers them, so a blocked reader would stall every request on the connection.
`sub.Dropped()` counts what was lost, and `WithSubscriptionBuffer` sets how much slack there is.

### Staying connected

A `Conn` cannot come back once iTerm2 quits or restarts: the cookie that authorised it was spent
on the handshake, and iTerm2 forgets every cookie when it restarts. `conn.Done()` closes when the
connection ends, and `conn.Err()` then says why. The answer is a fresh `Connect`:

```go
for ctx.Err() == nil {
	conn, err := iterm2.Connect(ctx)
	if err != nil {
		time.Sleep(time.Second) // iTerm2 not back yet
		continue
	}
	sub, err := conn.SubscribeNewSessions(ctx)
	// ... resynchronise your state: nothing posted while you were away is replayed
	<-conn.Done()
	log.Println("iTerm2 went away:", conn.Err())
}
```

With the default credentials that works in a process iTerm2 launched as well as in one it did
not. The `ITERM2_COOKIE` iTerm2 handed the process is used for the first connection only, and
later ones ask iTerm2 for a fresh cookie over AppleScript. That second path needs the process to
be allowed to send Apple Events to iTerm2, so macOS may ask the user once.

### Anything else in the API

The hand-written surface covers the session hierarchy, sending text, creating tabs and splits,
closing things, variables and notifications. Everything else iTerm2 can do — profiles, screen
contents, transactions, registering a toolbelt tool — goes through `Conn.Do`:

```go
resp, err := conn.Do(ctx, &apipb.ClientOriginatedMessage{
	Submessage: &apipb.ClientOriginatedMessage_RegisterToolRequest{
		RegisterToolRequest: &apipb.RegisterToolRequest{
			Name:       proto.String("My Tool"),
			Identifier: proto.String("com.example.mytool"),
			URL:        proto.String("http://localhost:8080/"),
		},
	},
})
```

`apipb` is generated from iTerm2's `api.proto`, so every request in the protocol is reachable
without waiting for a typed wrapper here.

## Errors

| Error | Means |
|---|---|
| `ErrUnauthorized` | iTerm2 refused the handshake: the API is off, or the cookie was already spent. Cookies are single use. |
| `ErrClosed` | The connection is gone, whether through `Close` or because iTerm2 quit. After a drop the error wraps `ErrClosed` and carries the cause, so `errors.Is(err, ErrClosed)` holds either way. |
| `*APIError` | iTerm2 could not parse the request, or answered with something this package cannot read. |
| `*StatusError` | A well-formed request iTerm2 declined. `Status` carries `api.proto`'s own spelling, so it can be looked up there. |

## Testing

```sh
go test ./...                       # the whole suite, no iTerm2 needed
go test -race ./...
ITERM2_LIVE=1 go test -run TestLive ./...   # against your real iTerm2: opens and closes a tab
```

The suite runs against `internal/fakeiterm`, which is not a mock of this package's internals: it
listens on a real unix socket, performs a real WebSocket upgrade, checks the same handshake
headers iTerm2 checks, and exchanges real protobuf. That is why it can run on Linux in CI.

What it cannot prove is that iTerm2 agrees with it, since both sides were written from the same
documents by the same author. `TestLive` is the test that closes that gap and it has never run
green — see [UPSTREAM.md](UPSTREAM.md). Treat the connection layer as unverified against the real
application.

## Contributing

`ROADMAP.md` is ordered by how much each entry limits real use; the top of it is the next thing
worth doing. Regenerating `apipb/` after the protocol moves needs [buf](https://buf.build):
`buf generate`. Never edit `proto/api.proto` — it is upstream's file, kept byte-identical so its
digest can be checked.

## License

**[GPL-2.0-or-later](LICENSE)** — copyleft. Linking this library into your program brings the
GPL's obligations with it, so read the licence before depending on it.

This is not a preference; it follows from what the library is. `proto/api.proto` is copied
verbatim from iTerm2, which is GPL-2.0, and iTerm2's own client bindings — the
[`iterm2` Python package](https://pypi.org/project/iterm2/), the direct equivalent of this
library — are published as **GPLv2+**. Relicensing a copy of someone else's protocol definition
more permissively is not ours to do.
