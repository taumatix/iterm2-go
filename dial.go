package iterm2

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/coder/websocket"
)

const (
	// Subprotocol is the WebSocket subprotocol iTerm2 requires. iTerm2 refuses
	// the upgrade unless sec-websocket-protocol matches it exactly
	// (iTermWebSocketConnection.m, kProtocolName).
	Subprotocol = "api.iterm2.com"

	// LibraryVersion is reported in the x-iterm2-library-version header as
	// "go <LibraryVersion>".
	//
	// iTerm2 requires the header to be two space-separated fields and compares
	// the second against a per-language minimum. It publishes a minimum for
	// "python" only, so "go" is not version-gated today — iTerm2's own Swift
	// client sends "swift 1.0" on the same basis. The value is a plain decimal
	// rather than this module's semantic version because that is what the
	// comparison parses; it is not a promise about anything else, and it moves
	// only if iTerm2 starts gating Go clients.
	LibraryVersion = "0.1"

	// LegacyTCPAddress is the loopback address iTerm2 listened on before the
	// unix domain socket, and still accepts. [Connect] falls back to it when the
	// socket is absent, which is what iTerm2's Python library does.
	LegacyTCPAddress = "localhost:1912"

	// defaultAdvisoryHeaderName is sent when the caller names nothing, so that
	// iTerm2's script console shows something better than an empty row.
	defaultAdvisoryHeaderName = "iterm2-go"
)

// DefaultSocketPath returns the unix domain socket a running iTerm2 listens on.
//
// It honours IT2_SUITE, which iTerm2 uses to keep parallel builds — a nightly
// alongside a release — from sharing one support directory.
func DefaultSocketPath() string {
	suite := os.Getenv("IT2_SUITE")
	if suite == "" {
		suite = "iTerm2"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// An unresolvable home directory yields a path that cannot exist, which
		// [Connect] reports as "no socket" and then falls back to TCP. Returning
		// an error here would put an error in the signature of a function whose
		// whole job is to name a default.
		return filepath.Join("Library", "Application Support", suite, "private", "socket")
	}
	return filepath.Join(home, "Library", "Application Support", suite, "private", "socket")
}

// handshakeHeader builds the HTTP headers iTerm2 validates on the upgrade
// request, beyond the ones the WebSocket library sets itself.
//
// Names are written lowercase to match iTerm2's source, though the case does
// not matter: iTerm2 lowercases every incoming header name as it parses the
// request (iTermHTTPConnection.m, -readRequestHeaders). Go's net/http
// canonicalises them to X-Iterm2-Cookie on the wire regardless.
func handshakeHeader(creds Credentials, advisoryName string) http.Header {
	if advisoryName == "" {
		advisoryName = defaultAdvisoryHeaderName
	}
	h := http.Header{}
	// iTerm2 requires an origin whose host is localhost, and rejects the upgrade
	// otherwise. Browsers would set this; we are not a browser, so we set it.
	h.Set("Origin", "ws://localhost/")
	h.Set("x-iterm2-library-version", "go "+LibraryVersion)
	h.Set("x-iterm2-advisory-name", advisoryName)
	// Both of iTerm2's own clients send this. Without it iTerm2 may put up a
	// modal asking the user to authorise the connection, which a library cannot
	// usefully wait on.
	h.Set("x-iterm2-disable-auth-ui", "true")
	if creds.Cookie != "" {
		h.Set("x-iterm2-cookie", creds.Cookie)
	}
	if creds.Key != "" {
		h.Set("x-iterm2-key", creds.Key)
	}
	return h
}

// dialWebSocket performs the upgrade against either a unix socket or a TCP
// address. The URL is always ws://localhost/ so that the Host header satisfies
// iTerm2's loopback check; where the bytes go is decided by the transport.
func dialWebSocket(ctx context.Context, network, address string, creds Credentials, advisoryName string) (*websocket.Conn, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			},
		},
	}

	url := "ws://localhost/"
	if network == "tcp" {
		url = "ws://" + address + "/"
	}

	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:   client,
		HTTPHeader:   handshakeHeader(creds, advisoryName),
		Subprotocols: []string{Subprotocol},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: iTerm2 refused the connection; check Settings > General > Magic > Enable Python API and that the cookie is fresh (%v)", ErrUnauthorized, err)
		}
		return nil, fmt.Errorf("iterm2: handshake with %s:%s failed: %w", network, address, err)
	}

	// iTerm2 always echoes the subprotocol. A peer that does not is not iTerm2,
	// and finding that out now beats failing to parse its first message.
	if got := ws.Subprotocol(); got != Subprotocol {
		ws.Close(websocket.StatusProtocolError, "unexpected subprotocol")
		return nil, fmt.Errorf("iterm2: peer negotiated subprotocol %q, want %q", got, Subprotocol)
	}
	return ws, nil
}
