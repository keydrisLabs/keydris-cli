package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// The channel→host binding lives in this package, in the shell installer, and in
// external edge routing. A binary pointing at a host that routing no longer maps
// has no upgrade path back, so these tests pin the two halves that live here to
// each other rather than to a literal.

func TestBaseURLForResolvesChannelHost(t *testing.T) {
	for channel, want := range channelBaseURL {
		if got := baseURLFor(channel, ""); got != want {
			t.Errorf("baseURLFor(%q, \"\") = %q, want %q", channel, got, want)
		}
		if got := baseURLFor(channel, "  "); got != want {
			t.Errorf("baseURLFor(%q, blank) = %q, want %q", channel, got, want)
		}
	}
	const override = "http://127.0.0.1:8080/keydris-cli"
	if got := baseURLFor("stable", override); got != override {
		t.Errorf("explicit base URL was not honored: got %q", got)
	}
}

func TestChannelsAreServedByDistinctHosts(t *testing.T) {
	if len(channelBaseURL) != 2 {
		t.Fatalf("expected exactly the stable and dev channels, got %v", channelBaseURL)
	}
	if channelBaseURL["stable"] == channelBaseURL["dev"] {
		t.Fatal("stable and dev must not share a host, or the channel binding is meaningless")
	}
}

func TestInstallerShipsStableBinding(t *testing.T) {
	script := readRepoFile(t, "install.sh")
	if got := bindingValue(t, script, "CHANNEL_DEFAULT"); got != "stable" {
		t.Errorf("install.sh CHANNEL_DEFAULT = %q, want stable (the checked-in copy is the stable flavor)", got)
	}
	if got, want := bindingValue(t, script, "BASE_DEFAULT"), channelBaseURL["stable"]; got != want {
		t.Errorf("install.sh BASE_DEFAULT = %q, but keydris upgrade uses %q", got, want)
	}
}

func TestRenderInstallMatchesChannelBaseURLs(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for channel, want := range channelBaseURL {
		out, err := exec.Command("bash", repoPath(t, "scripts/render-install.sh"), channel).Output()
		if err != nil {
			t.Fatalf("render-install.sh %s: %v", channel, err)
		}
		rendered := string(out)
		if got := bindingValue(t, rendered, "CHANNEL_DEFAULT"); got != channel {
			t.Errorf("rendered %s installer has CHANNEL_DEFAULT %q", channel, got)
		}
		if got := bindingValue(t, rendered, "BASE_DEFAULT"); got != want {
			t.Errorf("rendered %s installer has BASE_DEFAULT %q, but keydris upgrade uses %q", channel, got, want)
		}
	}
}

// bindingValue extracts a `KEY="value"` line from a rendered installer.
func bindingValue(t *testing.T, script, key string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + key + `="([^"]*)"$`)
	match := re.FindStringSubmatch(script)
	if match == nil {
		t.Fatalf("no %s binding line found; scripts/render-install.sh has nothing to substitute", key)
	}
	return match[1]
}

func repoPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(repoPath(t, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}
