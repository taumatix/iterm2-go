package iterm2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner stands in for osascript. It answers each script from a queue and
// records what it was asked to run, so the exchange can be checked without
// macOS or a running iTerm2.
type fakeRunner struct {
	replies []string
	err     error
	scripts []string
}

func (f *fakeRunner) run(_ context.Context, script string) (string, error) {
	f.scripts = append(f.scripts, script)
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", errors.New("fakeRunner: no reply queued")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	return reply, nil
}

func TestAppleScriptCredentialsParsesITerm2sReply(t *testing.T) {
	// iTerm2 answers the cookie and the key separated by one space.
	runner := &fakeRunner{replies: []string{"yes", "COOKIE-1 KEY-1"}}
	src := &appleScriptSource{advisoryName: "my-program", runner: runner}

	creds, err := src.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "COOKIE-1", creds.Cookie)
	assert.Equal(t, "KEY-1", creds.Key)

	require.Len(t, runner.scripts, 2)
	assert.Contains(t, runner.scripts[0], "is running", "it should check iTerm2 is running first")
	assert.Contains(t, runner.scripts[1], `request cookie and key for app named "my-program"`)
}

func TestAppleScriptCredentialsFailsWhenITerm2IsNotRunning(t *testing.T) {
	runner := &fakeRunner{replies: []string{"no"}}
	src := &appleScriptSource{advisoryName: "x", runner: runner}

	_, err := src.Credentials(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
	assert.Len(t, runner.scripts, 1, "it should not ask for a cookie")
}

func TestAppleScriptCredentialsRejectsAReplyItCannotParse(t *testing.T) {
	// Connecting unauthenticated on an unrecognised reply would turn a version
	// mismatch into a confusing handshake failure later.
	for name, reply := range map[string]string{
		"empty":        "",
		"one field":    "COOKIE-ONLY",
		"empty cookie": " KEY",
	} {
		t.Run(name, func(t *testing.T) {
			src := &appleScriptSource{
				advisoryName: "x",
				runner:       &fakeRunner{replies: []string{"yes", reply}},
			}
			_, err := src.Credentials(context.Background())
			assert.Error(t, err)
		})
	}
}

func TestAppleScriptCredentialsQuotesTheAdvisoryName(t *testing.T) {
	// iTerm2's own Python library interpolates this unescaped, so a quote there is
	// a syntax error and, with more care, could append statements.
	runner := &fakeRunner{replies: []string{"yes", "C K"}}
	src := &appleScriptSource{advisoryName: `evil" & (do shell script "id") & "`, runner: runner}

	_, err := src.Credentials(context.Background())
	require.NoError(t, err)

	script := runner.scripts[1]
	assert.Contains(t, script, `\"`, "quotes in the name must be escaped")
	// The name is one string literal, so the statement cannot be broken out of.
	assert.Equal(t, 1, strings.Count(script, "request cookie and key for app named"))
}

func TestAdvisoryNameWithANewlineIsRefused(t *testing.T) {
	// A newline ends the AppleScript statement, and escaping cannot save it.
	runner := &fakeRunner{replies: []string{"yes", "C K"}}
	src := &appleScriptSource{advisoryName: "one\nosascript -e whatever", runner: runner}

	_, err := src.Credentials(context.Background())
	require.Error(t, err)
	assert.Empty(t, runner.scripts, "nothing should have been run")
}

func TestQuoteAppleScriptEscapesBackslashesAndQuotes(t *testing.T) {
	assert.Equal(t, `"plain"`, quoteAppleScript("plain"))
	assert.Equal(t, `"say \"hi\""`, quoteAppleScript(`say "hi"`))
	assert.Equal(t, `"back\\slash"`, quoteAppleScript(`back\slash`))
}

func TestEnvCredentialsReportsErrNoCredentialsWhenNeitherIsSet(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "")
	t.Setenv("ITERM2_KEY", "")

	_, err := EnvCredentials().Credentials(context.Background())
	assert.ErrorIs(t, err, ErrNoCredentials)
}

func TestEnvCredentialsReadsWhatITerm2Set(t *testing.T) {
	t.Setenv("ITERM2_COOKIE", "env-cookie")
	t.Setenv("ITERM2_KEY", "env-key")

	creds, err := EnvCredentials().Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-cookie", creds.Cookie)
	assert.Equal(t, "env-key", creds.Key)
}

func TestDefaultCredentialsPrefersTheEnvironment(t *testing.T) {
	// A process iTerm2 launched already holds a cookie; asking for another over
	// AppleScript would raise a permission prompt for no reason.
	t.Setenv("ITERM2_COOKIE", "env-cookie")
	t.Setenv("ITERM2_KEY", "env-key")
	forgetSpentEnvCookie(t)

	creds, err := DefaultCredentials("x").Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-cookie", creds.Cookie)
}

func TestStaticCredentialsAlwaysYieldsTheSameValues(t *testing.T) {
	src := StaticCredentials("c", "k")
	for range 2 {
		creds, err := src.Credentials(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "c", creds.Cookie)
		assert.Equal(t, "k", creds.Key)
	}
}

func TestDefaultSocketPathHonoursIT2Suite(t *testing.T) {
	// iTerm2 uses IT2_SUITE to keep a nightly build's support directory separate
	// from a release's.
	t.Setenv("IT2_SUITE", "iTerm2-nightly")
	assert.Contains(t, DefaultSocketPath(), "iTerm2-nightly/private/socket")

	t.Setenv("IT2_SUITE", "")
	assert.Contains(t, DefaultSocketPath(), "iTerm2/private/socket")
}

func TestAppleScriptTargetHonoursIT2AppPath(t *testing.T) {
	t.Setenv("IT2_APP_PATH", "/Applications/iTerm2-nightly.app")
	assert.Equal(t, `application "/Applications/iTerm2-nightly.app"`, appleScriptTarget())

	t.Setenv("IT2_APP_PATH", "")
	assert.Equal(t, `application "iTerm2"`, appleScriptTarget())
}

func TestDefaultAdvisoryNameIsTheProgramsFilename(t *testing.T) {
	assert.Equal(t, "tool", baseName("/usr/local/bin/tool"))
	assert.Equal(t, "tool", baseName("tool"))
	assert.NotEmpty(t, defaultAdvisoryName())
}

func TestHandshakeHeaderNamesSomethingWhenTheCallerDoesNot(t *testing.T) {
	h := handshakeHeader(Credentials{Cookie: "c", Key: "k"}, "")
	assert.Equal(t, defaultAdvisoryHeaderName, h.Get("x-iterm2-advisory-name"))
	assert.Equal(t, "c", h.Get("x-iterm2-cookie"))
	assert.Equal(t, "k", h.Get("x-iterm2-key"))
}

func TestHandshakeHeaderOmitsCredentialsItDoesNotHave(t *testing.T) {
	// An empty cookie header is not the same as no cookie header, and iTerm2
	// invents a key when one is absent.
	h := handshakeHeader(Credentials{}, "name")
	_, hasCookie := h["X-Iterm2-Cookie"]
	assert.False(t, hasCookie)
	_, hasKey := h["X-Iterm2-Key"]
	assert.False(t, hasKey)
}
