package iterm2_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// upstreamPin is one entry of the yaml block in UPSTREAM.md. That block is the
// only place a pin is recorded, so that nothing can hold a second copy and
// disagree with it.
type upstreamPin struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`
	Repo    string `yaml:"repo"`
	Path    string `yaml:"path"`
	SHA     string `yaml:"sha"`
	SHA256  string `yaml:"sha256"`
	Checked string `yaml:"checked"`
}

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)\n```")

func readUpstreamPins(t *testing.T) []upstreamPin {
	t.Helper()
	doc, err := os.ReadFile("UPSTREAM.md")
	require.NoError(t, err, "UPSTREAM.md must exist: a version a user cannot see is one nobody can check")

	match := yamlBlock.FindSubmatch(doc)
	require.NotNil(t, match, "UPSTREAM.md has no ```yaml block for the pins")

	var pins []upstreamPin
	require.NoError(t, yaml.Unmarshal(match[1], &pins), "the pin block is not valid yaml")
	require.NotEmpty(t, pins)
	return pins
}

func pinNamed(t *testing.T, name string) upstreamPin {
	t.Helper()
	for _, pin := range readUpstreamPins(t) {
		if pin.Name == name {
			return pin
		}
	}
	t.Fatalf("UPSTREAM.md has no pin named %q", name)
	return upstreamPin{}
}

func TestUpstreamPinMatchesTheVendoredProto(t *testing.T) {
	// proto/api.proto is a verbatim copy of upstream's file, and the digest in
	// UPSTREAM.md is what says which copy. Editing one without the other makes the
	// published pin a lie, which is the failure this catches — offline, on every
	// CI run.
	pin := pinNamed(t, "iterm2-api-proto")
	require.NotEmpty(t, pin.SHA256, "the pin records no sha256")

	body, err := os.ReadFile("proto/api.proto")
	require.NoError(t, err)

	sum := sha256.Sum256(body)
	assert.Equal(t, pin.SHA256, hex.EncodeToString(sum[:]),
		"proto/api.proto does not match the sha256 in UPSTREAM.md; if the proto was "+
			"deliberately updated, update the pin (sha, sha256, upstream_date, checked) too")
}

func TestUpstreamPinsRecordWhenTheyWereChecked(t *testing.T) {
	// An unrefreshed date is indistinguishable from an unchecked one, so every pin
	// carries one and it has to be a date.
	for _, pin := range readUpstreamPins(t) {
		t.Run(pin.Name, func(t *testing.T) {
			require.NotEmpty(t, pin.Checked, "no checked date")
			_, err := time.Parse(time.DateOnly, pin.Checked)
			assert.NoError(t, err, "checked must be YYYY-MM-DD")
		})
	}
}

// TestUpstreamProtoHasNotDrifted asks GitHub whether the pinned commit is still
// the newest one touching api.proto.
//
// It needs the network, so it is gated: a CI run that cannot reach GitHub should
// not fail, and a drift report is maintenance work rather than a broken build.
// Run it with CHECK_UPSTREAM_DRIFT=1.
func TestUpstreamProtoHasNotDrifted(t *testing.T) {
	if os.Getenv("CHECK_UPSTREAM_DRIFT") != "1" {
		t.Skip("set CHECK_UPSTREAM_DRIFT=1 to ask GitHub whether the pin is current")
	}
	pin := pinNamed(t, "iterm2-api-proto")

	url := fmt.Sprintf("https://api.github.com/repos/%s/commits?path=%s&per_page=1", pin.Repo, pin.Path)
	req, err := http.NewRequestWithContext(testContext(t), http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GitHub answered %s", resp.Status)

	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
			Message string `json:"message"`
		} `json:"commit"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&commits))
	require.NotEmpty(t, commits)

	head := commits[0]
	assert.Equal(t, pin.SHA, head.SHA,
		"api.proto has moved upstream: %s (%s) %q. Re-copy it, run `buf generate`, "+
			"update UPSTREAM.md, and open a ROADMAP entry for any new surface",
		head.SHA, head.Commit.Author.Date, firstLine(head.Commit.Message))
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
