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

## 2. A `Session` keeps a session id that a restart may have changed

v0.4.0 lets a `Session` listed through a `Persistent` act after a reconnect, but it still names
the session by the id it was listed with. If iTerm2 restores sessions under new ids after a
restart (entry 2a asks), every held `Session` answers `SESSION_NOT_FOUND` and the caller must
list again on `Reconnects()`. Once entry 1 has the answer: either say so on `Session`, or have
`Persistent` re-resolve ids on reconnect (by tmux id or a tagged variable).

## 2a. Reconnecting has only been tested against the fake

The cookie behaviour v0.2.0 relies on is read from iTerm2's source at `5ed491d`
(`iTermWebSocketCookieJar`, `iTermAPIScriptLauncher`), and the fake reproduces it. No real iTerm2
has been restarted under a connected `Conn` or a `Persistent`. Three things need a real one. Does
the socket's read error surface promptly when iTerm2 quits, or only at the next write? Does
AppleScript cookie minting succeed in a process iTerm2 launched, which may not hold Apple Events
permission of its own? And does a per-session subscription survive a restart, which depends on
whether iTerm2 restores sessions under their old ids? If it does not, a `DurableSubscription` for
one session closes with `Err()` after every restart. Fold this into entry 1's live run.

## 3. Split a pane relative to the tree

v0.5.0 exposes the split tree read-only. `SplitPane` acts on one session (with `Before` and a
direction), so "split the right-hand column" means the caller picking a pane in it. Whether a
helper on `SplitNode` is worth having should wait for a real use. Also worth knowing from a real
iTerm2 (entry 1): whether a single-pane tab's root really is a split node holding one pane, which
the fake assumes.

## 4. Typed wrappers for the requests `Conn.Do` currently carries

Reachable today but unergonomic: profiles (`ListProfilesRequest`, `GetProfileProperty`,
`SetProfileProperty`), screen contents (`GetBufferRequest`), prompts, transactions, saved
arrangements, the main menu, and server-originated RPC. (`RegisterToolRequest` became
`RegisterTool` in v0.6.0.)

New methods go on `Conn` and `Persistent` but not on the `Client` interface: adding a method to an
exported interface breaks anyone who implements it. Whether `Client` should grow at a major
version, or be complemented by smaller interfaces, is open.

Session notes joined that list with upstream `5ed491d` (2026-09-26): a `"session_note"` session
property, `{ "text": string, "visible": boolean, "collapsed": boolean }`, read through
`GetPropertyRequest` and written through `SetPropertyRequest`, which accepts a partial object.
Nothing in the proto changed shape, so it is reachable via `Conn.Do` today; a typed
`Session.Note()` / `Session.SetNote()` pair is the wrapper.

Worth doing one at a time, each with its own tests, rather than as a sweep. Server-originated RPC
is typed for the generic role since v0.7.0. The other three roles each need their attributes:
`STATUS_BAR_COMPONENT` (descriptions, knobs, exemplar, update cadence) is the most visible,
`SESSION_TITLE` (display name, unique identifier) the simplest, and `CONTEXT_MENU` (display name).
Whether iTerm2 really matches a call on name plus argument names, and calls with every default
filled, is for entry 1's real run.

## 5. Track protocol additions rather than noticing them

`upstream_test.go` reports that `api.proto` has moved, which is the alarm. It does not say what
changed, so the answer to "does this matter" is still a manual diff. A check that named added
messages, fields and enum values would turn each protocol bump into a roadmap entry instead of an
investigation.

## Done

- **v0.7.0**: `RegisterRPC` (generic role), durable on `Persistent`. Fixed unsubscribe requests
  that dropped their arguments.

- **v0.6.0**: `RegisterTool`, typed, and kept across iTerm2 restarts by `Persistent`.

- **v0.5.0**: `Tab.SplitTree()` and `SplitNode`, mirroring api.proto's tree, with no invented
  geometry.

- **v0.4.0**: `Persistent` gained `Conn`'s typed surface and the `Client` interface both satisfy;
  sessions it lists act through it. `iterm2-claude-bridge` can drop its own `Link` (its roadmap).

- **v0.3.0**: the second half of reconnection. `ConnectPersistent` redials with backoff,
  re-makes `DurableSubscription`s on each new connection, and reports a `Reconnect` once they are
  back.
- **v0.2.0**: the first half of reconnection. `Conn.Done`/`Conn.Err`; `DefaultCredentials` spends
  the environment's cookie once and then asks over AppleScript; a refused environment cookie gets
  one fresh retry; `ErrClosed` now matches a drop from iTerm2's side, as it was documented to.

- **v0.1.0** — transport (unix socket and the legacy TCP fallback, handshake, cookie exchange over
  AppleScript), `Conn.Do` for the whole protocol, the session hierarchy, sending text, creating
  tabs and splits, closing, variables, notifications with per-subscription filtering. Tested
  against a fake iTerm2 over a real socket; `proto/api.proto` pinned and digest-checked.
