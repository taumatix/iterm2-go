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
go get github.com/taumatix/iterm2-go@v0.8.0
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

### How a tab's panes are arranged

`Tab.Sessions` lists a tab's panes in order: left to right, top to bottom. `Tab.SplitTree()` keeps
the arrangement, as iTerm2 reports it. Each node is either a pane (`Session` set) or a split whose
`Children` sit side by side (`Vertical`) or stacked. Its panes are the same `*Session` values as in
`Sessions`, and each carries its own `Frame`.

```go
func describe(n *iterm2.SplitNode, depth int) {
	pad := strings.Repeat("  ", depth)
	switch {
	case n.Session != nil:
		fmt.Printf("%spane %s\n", pad, n.Session.ID)
	case n.Vertical:
		fmt.Printf("%sside by side:\n", pad)
	default:
		fmt.Printf("%sstacked:\n", pad)
	}
	for _, c := range n.Children {
		describe(c, depth+1)
	}
}
```

### A panel in the toolbelt

`RegisterTool` puts a web page in iTerm2's toolbelt: serve it from your program (on loopback) and
register its URL.

```go
err := conn.RegisterTool(ctx, iterm2.Tool{
	Name:       "My panel",
	Identifier: "com.example.mypanel",
	URL:        "http://127.0.0.1:8080/",
	Reveal:     true, // show it even if it was registered, and hidden, before
})
```

iTerm2 forgets the tool when it quits. On a `Persistent`, `RegisterTool` also registers it again
on every new connection, without revealing it, and reports a refusal in `Reconnect.ToolErr`.

### Functions iTerm2 can call

`RegisterRPC` registers a function iTerm2 can invoke by name: from a key binding, a trigger, or
a toolbelt page. Its arguments arrive as JSON, and what it returns goes back as JSON. An error
goes back as an exception iTerm2 can show.

```go
reg, err := conn.RegisterRPC(ctx, iterm2.RPC{
	Name:      "greet",
	Arguments: []string{"who", "session_id"},
	Defaults:  map[string]string{"session_id": "session.id"}, // filled from an iTerm2 variable
	Handler: func(ctx context.Context, args map[string]json.RawMessage) (any, error) {
		var who string
		if err := json.Unmarshal(args["who"], &who); err != nil {
			return nil, err
		}
		return "hello " + who, nil
	},
})
```

On a `Persistent` the function is registered again on every new connection.

`RegisterTitleProvider` registers one as a session-title provider: it appears among the title
choices in a profile's settings under its `DisplayName`, and a session using it shows the string
its handler returns. Map an argument to a variable in `Defaults`, such as `user.gitBranch`; iTerm2
describes title providers as called again when such a variable changes, which this library's
tests (against a stand-in, not iTerm2) cannot show.

```go
reg, err := conn.RegisterTitleProvider(ctx, iterm2.TitleProvider{
	RPC: iterm2.RPC{
		Name:      "branch_title",
		Arguments: []string{"branch"},
		Defaults:  map[string]string{"branch": "user.gitBranch"},
		Handler: func(ctx context.Context, args map[string]json.RawMessage) (any, error) {
			var branch string
			_ = json.Unmarshal(args["branch"], &branch)
			return "⎇ " + branch, nil
		},
	},
	DisplayName: "Git branch",
	Identifier:  "com.example.branch-title",
})
```

Status-bar components and context-menu items are not typed yet.

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

A `Conn` cannot come back once iTerm2 quits or restarts, which it does on every update: the cookie
that authorised it was spent on the handshake, and iTerm2 forgets every cookie when it restarts.
`ConnectPersistent` does the reconnecting for you. It dials again when iTerm2 is back, re-makes
your subscriptions on the new connection, and tells you it did:

```go
p, err := iterm2.ConnectPersistent(ctx)
if err != nil {
	return err // the first connection failed: is the Python API enabled?
}
defer p.Close()

sub, err := p.SubscribeNewSessions(ctx) // its channel stays open across reconnects
if err != nil {
	return err
}
for {
	select {
	case n, ok := <-sub.C():
		if !ok {
			return sub.Err() // nil after Unsubscribe or Close
		}
		fmt.Println("new session:", n.GetNewSessionNotification().GetSessionId())
	case r := <-p.Reconnects():
		// Anything iTerm2 posted while it was away is lost. Resynchronise here:
		// subscriptions are already back, so nothing after this is missed.
		_, _ = p.ListSessions(ctx)
		log.Printf("reconnected (%d so far): %v", r.Count, r.Cause)
	}
}
```

A `Persistent` has every typed method a `Conn` has (`ListSessions`, `CreateTab`, `SendText`,
`SplitPane`, `Activate`, `Close*`, the variables, and each `Subscribe*` helper returning a
`DurableSubscription`), each made on whichever connection is current. A `Session`, `Tab` or
`Window` it lists acts through the `Persistent` too, so one you held across a restart still works.
Both satisfy `iterm2.Client`, so code written against that takes either.

While iTerm2 is away, every call returns `ErrReconnecting`, which also matches `ErrClosed`. A per-session subscription whose session did not survive the restart is closed with
`sub.Err()` saying why.

If you would rather write the loop yourself, `conn.Done()` closes when a `Conn` ends and
`conn.Err()` says why; a fresh `Connect` then makes a new one.

Either way, with the default credentials this works in a process iTerm2 launched as well as in one
it did not. The `ITERM2_COOKIE` iTerm2 handed the process is used for the first connection only, and
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
