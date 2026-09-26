// Package iterm2 is a Go client for the iTerm2 API.
//
// iTerm2 exposes the same API its Python scripting library uses: a WebSocket
// carrying protocol-buffer messages over a unix domain socket in the
// application's support directory. This package speaks that protocol directly,
// so nothing here needs Python, iTerm2's bundled runtime, or a script installed
// into iTerm2's scripts folder.
//
// # Enabling the API
//
// iTerm2 rejects every connection until the API is switched on in
// Settings > General > Magic > Enable Python API. The name says Python; the
// setting governs the socket, not the language.
//
// # Connecting
//
//	conn, err := iterm2.Connect(ctx)
//	if err != nil {
//		return err
//	}
//	defer conn.Close()
//
//	h, err := conn.ListSessions(ctx)
//	if err != nil {
//		return err
//	}
//	for _, s := range h.Sessions() {
//		fmt.Println(s.ID, s.Title)
//	}
//
// [Connect] authorises the connection with a single-use cookie. It reads
// ITERM2_COOKIE and ITERM2_KEY if they are set — which they are for a process
// iTerm2 launched itself — and otherwise asks the running iTerm2 for a fresh
// pair over AppleScript, which requires macOS. Pass [WithCredentials] to supply
// them yourself.
//
// # Two layers
//
// The hand-written surface — [Conn], [Hierarchy], [Window], [Tab], [Session]
// and their methods — is the stable one, and covers the session hierarchy,
// sending text, creating tabs and splits, closing things, variables and
// notifications.
//
// Everything else iTerm2 can do is reachable through [Conn.Do], which sends one
// ClientOriginatedMessage from the apipb package and returns its response. That
// includes profiles, the screen contents, transactions, registering a toolbelt
// tool and the rest of api.proto. apipb is generated from iTerm2's api.proto and
// tracks it: UPSTREAM.md records the commit it came from, what is verified, and
// what is not.
//
// # Concurrency
//
// A [Conn] is safe for concurrent use. Requests from many goroutines are
// serialised onto the socket and matched to their responses by message id, so
// one slow request does not block another.
//
// Notifications are delivered to a [Subscription]'s channel and are dropped
// rather than blocking when the consumer falls behind, because the read loop
// delivers them and a blocked reader would stall every request on the
// connection. [Subscription.Dropped] counts what was lost.
package iterm2
