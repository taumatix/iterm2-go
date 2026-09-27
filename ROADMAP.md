# Roadmap

Ordered by how much each entry limits real use. The top is the next thing worth doing unless
there is a reason to say otherwise.

## 1. Run `TestLive` green against a real iTerm2

Nothing in this library has ever talked to iTerm2. The suite drives `internal/fakeiterm`, whose
expectations were written from `api.proto` and iTerm2's source by the same author as the client —
so it proves self-consistency, not agreement. `TestLive` exists and is written; it has never run.

It is blocked on two things a human at the machine must do:

- **Enable the API** in Settings → General → Magic → Enable Python API. There is no socket at
  `~/Library/Application Support/iTerm2/private/socket` without it.
- **Grant Apple Events permission** to whatever shell runs the test. On the host this was built
  on, `osascript` reports iTerm2 not running while it plainly is, and `System Events` fails with
  `-10810` — the signature of a denied Apple Event.

Three guesses are waiting on that run, each marked where it is made:

- `NewTab.TabID` renders `CreateTabResponse.tab_id` (`int32`) as a decimal string on the
  assumption it names the same tab as `ListSessionsResponse.Tab.tab_id` (`string`). `TestLive`
  asserts the created tab is findable by that id.
- `application "iTerm2"` is the AppleScript target for the cookie request, though the bundle is
  `/Applications/iTerm.app`. If that name is wrong, `Connect` cannot authorise at all outside a
  process iTerm2 launched.
- `x-iterm2-library-version: go 0.1` is assumed not to be version-gated, since iTerm2 publishes a
  minimum for `python` only.

Until this is done, the README and `UPSTREAM.md` both say the connection layer is unverified, and
they should keep saying it.

## 2. Reconnection, because a cookie is single use

`Connect` authorises with a cookie iTerm2 consumes during the handshake, so a `Conn` cannot
reconnect itself: the credentials it was built with are spent. A long-running program that
survives an iTerm2 restart therefore has to notice `ErrClosed`, build a fresh
`CredentialSource`, and re-subscribe to everything — all of which it must write itself today.

A `Reconnect` or a self-healing wrapper needs a decision about subscriptions: re-subscribing
silently would hide a gap in the notification stream, and not re-subscribing makes the wrapper
useless. Probably: re-subscribe, and report the gap on the channel.

## 3. Expose the split tree

`Tab.Sessions` is flattened from `api.proto`'s `SplitTreeNode` in tree order, which is what most
callers want, and the tree's shape is currently dropped. Anything that needs to know *how* panes
are arranged — which pane is left of which, how a tab would look redrawn — cannot get it.

Additive: keep `Sessions`, add `Tab.SplitTree()`. The geometry types are the design question, and
the reason this is not in v0.1.0: getting them wrong is expensive to undo.

## 4. Typed wrappers for the requests `Conn.Do` currently carries

Reachable today but unergonomic: profiles (`ListProfilesRequest`, `GetProfileProperty`,
`SetProfileProperty`), screen contents (`GetBufferRequest`), prompts, transactions,
`RegisterToolRequest`, saved arrangements, the main menu, and server-originated RPC.

Session notes joined that list with upstream `5ed491d` (2026-09-26): a `"session_note"` session
property, `{ "text": string, "visible": boolean, "collapsed": boolean }`, read through
`GetPropertyRequest` and written through `SetPropertyRequest`, which accepts a partial object.
Nothing in the proto changed shape, so it is reachable via `Conn.Do` today; a typed
`Session.Note()` / `Session.SetNote()` pair is the wrapper.

Worth doing one at a time, each with its own tests, rather than as a sweep. `RegisterToolRequest`
is the most valuable: it is what lets a Go program put its own panel in iTerm2's toolbelt.

## 5. Track protocol additions rather than noticing them

`upstream_test.go` reports that `api.proto` has moved, which is the alarm. It does not say what
changed, so the answer to "does this matter" is still a manual diff. A check that named added
messages, fields and enum values would turn each protocol bump into a roadmap entry instead of an
investigation.

## Done

- **v0.1.0** — transport (unix socket and the legacy TCP fallback, handshake, cookie exchange over
  AppleScript), `Conn.Do` for the whole protocol, the session hierarchy, sending text, creating
  tabs and splits, closing, variables, notifications with per-subscription filtering. Tested
  against a fake iTerm2 over a real socket; `proto/api.proto` pinned and digest-checked.
