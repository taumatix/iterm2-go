# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-10-04

### Added

- **`ConnectPersistent`, a connection that survives iTerm2 restarting.** iTerm2 restarts on every
  update, and a `Conn` cannot come back from that. `Persistent` dials again when the connection
  ends, waiting one second and doubling to thirty (`WithReconnectBackoff`). It uses fresh
  credentials each time, and re-makes every subscription on the new connection before announcing
  it.
- `Persistent.Subscribe`, `SubscribeNewSessions` and `SubscribeTerminatedSessions` return a
  `DurableSubscription`, whose channel stays open across reconnects. It closes on `Unsubscribe`,
  on `Close`, or when iTerm2 refuses the subscription on a new connection, with `Err()` saying
  why.
- `Persistent.Reconnects()` reports each reconnect as a `Reconnect` with a running `Count`, the
  `Cause` and the time, sent once the subscriptions are back. Notifications posted while iTerm2
  was away are lost; this is the signal to resynchronise. The channel keeps only the latest, so a
  slow reader sees the count jump rather than blocking the reconnect.
- `ErrReconnecting`, returned by `Persistent.Conn` and `Persistent.Do` while iTerm2 is away. It
  wraps `ErrClosed`.
- `WithReconnectBackoff`, read by `ConnectPersistent` only.

### Known limitation

Tested against the fake over a real unix socket, including the single-use cookie path, not against
a real iTerm2 restarting. That is ROADMAP entry 2a.

## [0.2.0] - 2026-09-28

### Fixed

- **A second `Connect` in a process iTerm2 launched could never succeed.** `DefaultCredentials`
  returned `ITERM2_COOKIE` on every call. iTerm2 issues that cookie single use
  (`iTermWebSocketCookieJar consumeCookie`, checked at `5ed491d`) and forgets every cookie when it
  restarts, so reconnecting presented a spent cookie and got `ErrUnauthorized` every time, without
  ever trying AppleScript. The environment's cookie is now used once per process, and later
  attempts ask iTerm2 for a fresh cookie. A cookie refused on the first attempt (inherited from a
  parent that had already spent it) gets one retry with a fresh cookie, as iTerm2's Python library
  does. A refused *fresh* cookie is not retried, because that would only prompt the user again.
- **`ErrClosed` did not match when iTerm2 quit.** It is documented as what every operation returns
  once the connection is gone, whether through `Close` or because iTerm2 quit. After a drop,
  operations returned only the read error, so `errors.Is(err, ErrClosed)` was false in exactly the
  case it was written for. The error now wraps `ErrClosed` together with the cause.

### Added

- `Conn.Done()`, closed when the connection ends, and `Conn.Err()`, which says why. A program that
  only listens has no request in flight to fail, so before this it could not notice iTerm2 had
  gone. The README has the reconnect loop.

### Changed

- `proto/api.proto` tracks iTerm2 `5ed491d` (2026-09-26). The change is comments only: it
  documents the `"session_note"` property on `GetPropertyRequest` and `SetPropertyRequest`.
- `google.golang.org/protobuf` v1.36.6 → v1.36.12, and `apipb/` regenerated with the matching
  `protoc-gen-go`.

## [0.1.0] - 2026-09-26

First release. A Go client for iTerm2's API, speaking its WebSocket protocol directly.

### Added

- `Connect` over the unix domain socket in iTerm2's support directory, falling back to the legacy
  loopback TCP address when no socket exists, as iTerm2's own Python library does. Honours
  `IT2_SUITE` so a nightly build and a release do not share a support directory.
- Authorisation with a single-use cookie: from `ITERM2_COOKIE` / `ITERM2_KEY` when iTerm2 launched
  the process, otherwise by asking the running iTerm2 over AppleScript.
- `Conn.Do`, which sends any `ClientOriginatedMessage` and returns its response, so the whole
  protocol is reachable without waiting for a typed wrapper.
- The session hierarchy: `ListSessions` returning `Hierarchy`, `Window`, `Tab` and `Session`, with
  panes flattened from `api.proto`'s split tree, tab groups (protocol 1.19), tmux identifiers,
  buried and minimized sessions, and lookups by id.
- `SendText`, `CreateTab`, `SplitPane`, `Activate`, and `CloseSessions` / `CloseTabs` /
  `CloseWindows` with per-target statuses.
- Variables: `GetVariables` and `SetVariables` across the session, tab, window and app scopes,
  with string convenience wrappers that handle the JSON encoding.
- Notifications: `Subscribe` plus named helpers, with each `Subscription` filtering to what it
  asked for, since iTerm2 posts everything down one socket with nothing tying a notification back
  to its request. Notifications are dropped rather than blocking; `Subscription.Dropped` counts
  them.
- `internal/fakeiterm`, a fake iTerm2 on a real unix socket with a real WebSocket upgrade, real
  protobuf and iTerm2's handshake checks — which is what lets the suite run on Linux in CI.
- `UPSTREAM.md`, recording the `api.proto` commit and digest, enforced offline by
  `upstream_test.go` on every CI run, with a network-gated check for whether the pin is current.

### Known limitations

- **The connection layer is not verified against a real iTerm2.** Every test drives the fake.
  `TestLive` covers the real application and is gated on `ITERM2_LIVE=1`; it has never run green,
  because the host this was built on has the Python API switched off and denies Apple Events to
  its shell. `UPSTREAM.md` lists the three specific assumptions that run would settle, and
  `ROADMAP.md` entry 1 tracks it.
- A `Conn` cannot reconnect: the cookie it authorised with is consumed by the handshake. See
  `ROADMAP.md` entry 2.
- The split tree's shape is not exposed, only the flattened pane order. See `ROADMAP.md` entry 3.

[Unreleased]: https://github.com/taumatix/iterm2-go/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/taumatix/iterm2-go/releases/tag/v0.3.0
[0.2.0]: https://github.com/taumatix/iterm2-go/releases/tag/v0.2.0
[0.1.0]: https://github.com/taumatix/iterm2-go/releases/tag/v0.1.0
