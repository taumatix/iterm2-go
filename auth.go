package iterm2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Credentials authorise a connection to iTerm2's API.
//
// The cookie is single use: iTerm2 consumes it during the WebSocket handshake
// (iTermWebSocketCookieJar consumeCookie), so a second connection needs a
// second cookie. The key is a stable identity for the client — iTerm2 reuses
// it to recognise a reconnecting script and offer the same authorisation, and
// invents one if it is absent.
type Credentials struct {
	Cookie string
	Key    string
}

// CredentialSource supplies credentials for one connection attempt.
//
// It is called once per attempt rather than once per Conn, because a cookie is
// consumed by the attempt whether or not the attempt succeeds.
type CredentialSource interface {
	Credentials(ctx context.Context) (Credentials, error)
}

// CredentialSourceFunc adapts a function to [CredentialSource].
type CredentialSourceFunc func(ctx context.Context) (Credentials, error)

// Credentials calls f.
func (f CredentialSourceFunc) Credentials(ctx context.Context) (Credentials, error) {
	return f(ctx)
}

// ErrNoCredentials is returned by [EnvCredentials] when neither ITERM2_COOKIE
// nor ITERM2_KEY is set.
var ErrNoCredentials = errors.New("iterm2: ITERM2_COOKIE and ITERM2_KEY are not set")

// StaticCredentials returns a source that always yields the same values.
//
// Because a cookie is single use, a Conn built on this source can connect once.
// It is meant for a process iTerm2 launched — which is handed a cookie in its
// environment — and for tests.
func StaticCredentials(cookie, key string) CredentialSource {
	return CredentialSourceFunc(func(context.Context) (Credentials, error) {
		return Credentials{Cookie: cookie, Key: key}, nil
	})
}

// EnvCredentials reads ITERM2_COOKIE and ITERM2_KEY from the environment.
//
// iTerm2 sets both for processes it launches itself, including scripts run from
// its Scripts menu. It returns [ErrNoCredentials] when neither is set, so it
// can be chained ahead of [AppleScriptCredentials] — which is what
// [DefaultCredentials] does.
func EnvCredentials() CredentialSource {
	return CredentialSourceFunc(func(context.Context) (Credentials, error) {
		cookie, key := os.Getenv("ITERM2_COOKIE"), os.Getenv("ITERM2_KEY")
		if cookie == "" && key == "" {
			return Credentials{}, ErrNoCredentials
		}
		return Credentials{Cookie: cookie, Key: key}, nil
	})
}

// AppleScriptCredentials asks a running iTerm2 for a fresh single-use cookie.
//
// advisoryName is shown to the user in iTerm2's script console and in the
// permission prompt, so it should name the program rather than the library.
//
// This requires macOS and a running iTerm2: it drives /usr/bin/osascript, and
// on any other platform it fails without running anything. The user may be
// asked to approve the connection the first time a given key appears.
//
// IT2_APP_PATH, if set, selects which iTerm2 build to ask — matching the
// behaviour of iTerm2's own it2 CLI, and needed only when several are running.
func AppleScriptCredentials(advisoryName string) CredentialSource {
	return &appleScriptSource{advisoryName: advisoryName, runner: osascriptRunner{}}
}

// DefaultCredentials uses the environment when iTerm2 has provided credentials
// there, and otherwise asks iTerm2 for a fresh cookie over AppleScript.
//
// The environment's cookie is used once per process. iTerm2 issues it single
// use and forgets every cookie when it restarts, so presenting it a second time
// can only be refused; after the first attempt this source asks over
// AppleScript instead, which is what lets a program reconnect. And when the
// environment's cookie is refused outright — inherited from a parent that
// already spent it — [Connect] asks for a fresh one and tries once more. Both
// match iTerm2's Python library, which deletes ITERM2_COOKIE after a refusal;
// this one leaves the environment alone and remembers the spent value instead.
func DefaultCredentials(advisoryName string) CredentialSource {
	return defaultCredentials(advisoryName, osascriptRunner{})
}

func defaultCredentials(advisoryName string, runner scriptRunner) CredentialSource {
	return &defaultSource{
		env:    EnvCredentials(),
		script: &appleScriptSource{advisoryName: advisoryName, runner: runner},
	}
}

// spentEnvCookie is the environment cookie this process has already presented.
// Process-wide, because each [Connect] builds its own default source and the
// cookie is spent for all of them.
var spentEnvCookie spentCookie

type spentCookie struct {
	mu    sync.Mutex
	value string
}

// claim reports whether cookie is still unspent, and marks it spent if so.
func (s *spentCookie) claim(cookie string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cookie == s.value {
		return false
	}
	s.value = cookie
	return true
}

func (s *spentCookie) forget() {
	s.mu.Lock()
	s.value = ""
	s.mu.Unlock()
}

type defaultSource struct {
	env    CredentialSource
	script CredentialSource

	mu      sync.Mutex
	fromEnv bool
}

func (d *defaultSource) Credentials(ctx context.Context) (Credentials, error) {
	creds, err := d.env.Credentials(ctx)
	switch {
	case err == nil && creds.Cookie == "":
		// A key with no cookie: nothing to spend, and what this did before.
		d.setFromEnv(false)
		return creds, nil
	case err == nil && spentEnvCookie.claim(creds.Cookie):
		d.setFromEnv(true)
		return creds, nil
	case err != nil && !errors.Is(err, ErrNoCredentials):
		return Credentials{}, err
	}
	d.setFromEnv(false)
	return d.script.Credentials(ctx)
}

// retryAfterUnauthorized reports whether a refused handshake is worth one more
// attempt: only when the refused cookie came from the environment, since the
// next call will ask iTerm2 for a fresh one. A refused fresh cookie means the
// API is off or the user declined, and asking again would only prompt again.
func (d *defaultSource) retryAfterUnauthorized() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fromEnv
}

func (d *defaultSource) setFromEnv(v bool) {
	d.mu.Lock()
	d.fromEnv = v
	d.mu.Unlock()
}

// scriptRunner executes an AppleScript and returns its result. It exists so the
// cookie exchange can be tested without macOS or a running iTerm2.
type scriptRunner interface {
	run(ctx context.Context, script string) (string, error)
}

type osascriptRunner struct{}

func (osascriptRunner) run(ctx context.Context, script string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("iterm2: AppleScript authorisation needs macOS, not %s; pass WithCredentials to supply a cookie another way", runtime.GOOS)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("iterm2: osascript failed: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("iterm2: osascript failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

type appleScriptSource struct {
	advisoryName string
	runner       scriptRunner
}

func (s *appleScriptSource) Credentials(ctx context.Context) (Credentials, error) {
	name := s.advisoryName
	if name == "" {
		name = defaultAdvisoryName()
	}
	if err := validateAdvisoryName(name); err != nil {
		return Credentials{}, err
	}

	target := appleScriptTarget()
	running, err := s.runner.run(ctx, fmt.Sprintf(
		"if %s is running then\n\treturn \"yes\"\nelse\n\treturn \"no\"\nend if", target))
	if err != nil {
		return Credentials{}, err
	}
	if running != "yes" {
		// A denied Apple Event is indistinguishable from a quit application here:
		// AppleScript answers false either way rather than raising. Naming both
		// causes beats asserting the one we cannot tell apart — a sandboxed shell
		// with no automation permission lands here with iTerm2 plainly running.
		return Credentials{}, fmt.Errorf("iterm2: %s reports iTerm2 is not running; it may instead be that this process is not permitted to send Apple Events (check System Settings > Privacy & Security > Automation)", target)
	}

	out, err := s.runner.run(ctx, fmt.Sprintf(
		"tell %s to request cookie and key for app named %s", target, quoteAppleScript(name)))
	if err != nil {
		return Credentials{}, err
	}

	// iTerm2 answers with the cookie and the key separated by one space. Anything
	// else means we are talking to a version whose reply we do not understand,
	// which is worth saying rather than silently connecting unauthenticated.
	cookie, key, ok := strings.Cut(out, " ")
	if !ok || cookie == "" || key == "" {
		return Credentials{}, fmt.Errorf("iterm2: could not parse cookie and key from iTerm2's reply (%d bytes, %d fields)",
			len(out), len(strings.Fields(out)))
	}
	return Credentials{Cookie: cookie, Key: key}, nil
}

// appleScriptTarget names the application clause cookie requests are sent to.
// IT2_APP_PATH disambiguates between simultaneously running iTerm2 builds.
func appleScriptTarget() string {
	if path := os.Getenv("IT2_APP_PATH"); path != "" {
		return "application " + quoteAppleScript(path)
	}
	return `application "iTerm2"`
}

// quoteAppleScript renders s as an AppleScript string literal.
//
// iTerm2's own Python library interpolates the script name unescaped, so a name
// containing a quote produces a syntax error there and, with more care, could
// append statements. Escaping costs four lines.
func quoteAppleScript(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// validateAdvisoryName rejects names that cannot appear in an AppleScript string
// literal at all. A newline ends the statement, and escaping cannot save it.
func validateAdvisoryName(name string) error {
	if strings.ContainsAny(name, "\n\r") {
		return fmt.Errorf("iterm2: advisory name must not contain a newline")
	}
	return nil
}

// defaultAdvisoryName is what iTerm2 shows when the caller names nothing: the
// program's own filename, matching what the Python library reports.
func defaultAdvisoryName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		if base := baseName(os.Args[0]); base != "" {
			return base
		}
	}
	return "Unknown"
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
