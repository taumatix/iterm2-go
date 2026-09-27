# Upstream

This library speaks iTerm2's API, and the wire format is iTerm2's to change. `proto/api.proto`
is a **byte-identical copy of iTerm2's own file**, and `apipb/` is generated from it. This file
says which version it was copied from and when that was last checked, so you can judge how
current the port is before depending on it.

The block below is the source of truth. `upstream_test.go` parses it and fails if the checked-in
proto stops matching the recorded digest, so the pin cannot drift from the file it describes
without CI noticing.

```yaml
- name: iterm2-api-proto
  kind: github-file
  repo: gnachman/iTerm2
  path: proto/api.proto
  sha: 5ed491d0cc7bba5a307f8ec70b6707f8c5e57956
  sha256: e6c7fb443038d5e52b1721f9c151a4c53ba242986f8a391ecd2661349094f958
  upstream_date: 2026-09-26
  checked: 2026-09-27
  note: >-
    the protocol definition. Copied verbatim and verified byte-identical against
    upstream at this commit on 2026-09-27 (upstream commit "Add Python API support
    for Session Notes"), which was the newest commit touching the path. Carries
    protocol 1.19, so tab groups and the 1.18 selected_tab_id / active_session_id
    fields are present. The move from a23c8c3 changed comments only: a
    "session_note" property is documented for Get/SetPropertyRequest, which carry
    property names as strings, so no message or field changed. Nothing in this
    repo may edit the file: buf.gen.yaml injects the go_package option instead, so
    the copy stays diffable.

- name: iterm2-app
  kind: macos-app
  minimum: "3.5.0"
  checked: 2026-09-27
  hold: >-
    not verified against a running iTerm2 by any automated run yet. The socket path,
    the handshake headers and the AppleScript cookie exchange were written from
    iTerm2's source and api.proto, and are exercised only against
    internal/fakeiterm. TestLive covers the real app and is gated on ITERM2_LIVE=1;
    it has not been run green, because this host has the Python API switched off and
    its shell is denied Apple Events. See ROADMAP.md.
```

## What is and is not verified

**Verified on 2026-09-27.** `proto/api.proto` is byte-identical to upstream at the pinned commit:
both sides hash to `e6c7fb443038d5e52b1721f9c151a4c53ba242986f8a391ecd2661349094f958`. The pinned
commit was the newest one touching `proto/api.proto` at that date.

**Not verified: that this library talks to iTerm2 at all.** Every test drives
`internal/fakeiterm`, which implements the protocol as read from `api.proto` and iTerm2's source —
a real unix socket, a real WebSocket upgrade, real protobuf, and the same header checks iTerm2
makes. That proves the client is self-consistent and catches framing, id-matching and
notification-routing faults. It cannot prove iTerm2 agrees, because the expectations on both sides
were written by the same author from the same documents.

`TestLive` is the test that would close that gap, and it has never run green. This host has
**Enable Python API switched off** (no socket at
`~/Library/Application Support/iTerm2/private/socket`) and its shell is **denied Apple Events**
(`osascript` reports iTerm2 not running, and `System Events` fails with `-10810`, while iTerm2 is
plainly running). Both need a human at the machine. Until then, treat the connection layer as
unverified against the real application, and read `ROADMAP.md` entry 1.

Three specific guesses would be settled by that run, and are called out where they are made:

- `NewTab.TabID` renders `CreateTabResponse.tab_id`, an `int32`, as a decimal string, on the
  assumption that it names the same tab as `ListSessionsResponse.Tab.tab_id`, a `string`.
  `TestLive` asserts the created tab is findable by that id.
- `application "iTerm2"` is the AppleScript target for the cookie request, though the bundle on
  this host is `/Applications/iTerm.app`.
- `x-iterm2-library-version: go 0.1` is assumed not to be version-gated, since iTerm2 publishes a
  minimum for `python` only.

## Checking for drift

    go test -run TestUpstream ./...                      # offline: the pin matches the vendored file
    CHECK_UPSTREAM_DRIFT=1 go test -run TestUpstream ./...  # also asks GitHub whether the pin is current

Regenerating `apipb/` after the proto moves needs [buf](https://buf.build):

    buf generate

`buf.yaml` turns lint and breaking-change checks off for `proto/`: the file is upstream's, so a
lint failure there would be a complaint about someone else's style, and a breaking-change failure
would fire every time iTerm2 adds a field — which is the event the drift check exists to report,
in a form that says what changed.
