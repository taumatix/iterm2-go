# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/taumatix/iterm2-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/taumatix/iterm2-go/releases/tag/v0.1.0
