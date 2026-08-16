package temppaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/pathutil"
)

func TestResolverClassifiesInsideOutsideAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	resolver := New(root)

	state, candidate, _, err := resolver.Classify(filepath.Join(root, "new", "file.txt"))
	if err != nil || state != Inside || candidate.Host == "" {
		t.Fatalf("inside classification = %q %#v err=%v", state, candidate, err)
	}
	state, _, _, err = resolver.Classify(filepath.Join(outside, "file.txt"))
	if err != nil || state != Outside {
		t.Fatalf("outside classification = %q err=%v", state, err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	state, candidate, _, err = resolver.Classify(filepath.Join(link, "file.txt"))
	wantOutside, canonicalErr := pathutil.Canonicalize(filepath.Join(outside, "file.txt"))
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || state != Escape || candidate.Host != wantOutside.Host {
		t.Fatalf("escape classification = %q %#v err=%v", state, candidate, err)
	}
}

func TestResolverPrimaryAndCanonicalRootDeduplication(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolver := New(alias, root)
	if got := len(resolver.Roots()); got != 1 {
		t.Fatalf("canonical roots = %d, want 1", got)
	}
	primary, ok := resolver.Primary()
	wantRoot, err := pathutil.Canonicalize(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || primary.Host != wantRoot.Host {
		t.Fatalf("primary = %#v ok=%t, want %q", primary, ok, wantRoot.Host)
	}
	state, candidate, _, err := resolver.ResolveAtPrimary("nested/file.txt")
	if err != nil || state != Inside || candidate.Host != filepath.Join(wantRoot.Host, "nested", "file.txt") {
		t.Fatalf("resolved primary = %q %#v err=%v", state, candidate, err)
	}
}

func TestSystemResolverContainsProcessTempDir(t *testing.T) {
	target, err := os.CreateTemp("", "agent-platform-temp-root-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	path := target.Name()
	_ = target.Close()
	t.Cleanup(func() { _ = os.Remove(path) })
	state, _, _, err := System().Classify(path)
	if err != nil || state != Inside {
		t.Fatalf("system temp classification = %q err=%v", state, err)
	}
}

func TestDarwinTmpAndPrivateTmpAreEquivalent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only alias contract")
	}
	resolver := System()
	left, _, _, leftErr := resolver.Classify("/tmp/agent-platform-alias.txt")
	right, _, _, rightErr := resolver.Classify("/private/tmp/agent-platform-alias.txt")
	if leftErr != nil || rightErr != nil || left != Inside || right != Inside {
		t.Fatalf("tmp aliases = %q/%q errors=%v/%v", left, right, leftErr, rightErr)
	}
}

func TestSystemResolverIsFrozenBeforeTempEnvironmentChanges(t *testing.T) {
	before, ok := System().Primary()
	if !ok {
		t.Fatal("system temporary root is unavailable")
	}
	other := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("TEMP", other)
		t.Setenv("TMP", other)
	} else {
		t.Setenv("TMPDIR", other)
	}
	after, ok := System().Primary()
	if !ok || after.Key != before.Key {
		t.Fatalf("system temporary root changed after environment mutation: before=%#v after=%#v", before, after)
	}
}

func TestWindowsSystemResolverUsesOnlyProcessTempRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only temporary root contract")
	}
	resolver := System()
	if got := len(resolver.Roots()); got != 1 {
		t.Fatalf("Windows temporary roots=%d, want only os.TempDir", got)
	}
	primary, ok := resolver.Primary()
	if !ok {
		t.Fatal("system temporary root is unavailable")
	}
	caseVariant := strings.ToUpper(filepath.Join(primary.Host, "agent-platform", "note.txt"))
	state, _, _, err := resolver.Classify(caseVariant)
	if err != nil || state != Inside {
		t.Fatalf("Windows case-insensitive temporary path state=%s err=%v", state, err)
	}
	volume := filepath.VolumeName(primary.Host)
	other := filepath.Join(volume+string(filepath.Separator), "agent-platform-not-process-temp", "note.txt")
	state, _, _, err = resolver.Classify(other)
	if err != nil || state != Outside {
		t.Fatalf("unconfigured Windows temporary-looking path state=%s err=%v", state, err)
	}
}
