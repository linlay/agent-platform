package shellenv

import (
	"strings"
	"testing"
)

func TestInheritedEnvironmentUsesConfiguredNamesAndPrefixes(t *testing.T) {
	host := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy", "LC_ALL=C", "XDG_CONFIG_HOME=/x", "SECRET_TOKEN=s", "LD_PRELOAD=evil.so", "NODE_OPTIONS=--require x", "GIT_CONFIG_COUNT=1", "HOME=/home/u"}
	got := strings.Join(InheritedEnvironment(host, "PATH", "HTTPS_PROXY", "LC_*", "XDG_*", "HOME", "LD_PRELOAD", "NODE_OPTIONS", "GIT_*"), " ")
	for _, want := range []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy", "LC_ALL=C", "XDG_CONFIG_HOME=/x", "HOME=/home/u"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	for _, banned := range []string{"SECRET_TOKEN", "LD_PRELOAD", "NODE_OPTIONS", "GIT_CONFIG_COUNT"} {
		if strings.Contains(got, banned) {
			t.Fatalf("%s must never be inherited: %s", banned, got)
		}
	}
}
