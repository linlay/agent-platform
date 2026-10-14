package connector

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/pathutil"
)

func TestWindowsCLIEntriesFreezeNodeShimAndActualScript(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	script := filepath.Join(bin, "node_modules", "@wecom", "cli", "bin", "wecom.js")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("fixture script"), 0600); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "wecom-cli.cmd")
	if err := os.WriteFile(shim, []byte(`@"%_prog%" "%~dp0\node_modules\@wecom\cli\bin\wecom.js" %*`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := snapshotCLIEntries("wecom", root, "windows")
	if err != nil || len(entries) != 1 {
		t.Fatalf("freeze Windows shim: %v %+v", err, entries)
	}
	entry := entries[0]
	canonical, err := pathutil.Canonicalize(script)
	if err != nil || entry.LauncherTarget != canonical.Host || entry.LauncherTargetKey != canonical.Key || len(entry.LauncherTargetSHA256) != 64 {
		t.Fatalf("actual script was not frozen: %+v %v", entry, err)
	}
	oldScriptHash := entry.LauncherTargetSHA256
	if err := os.WriteFile(script, []byte("changed script"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := snapshotCLIEntries("wecom", root, "windows")
	if err != nil || fresh[0].SHA256 != entry.SHA256 || fresh[0].LauncherTargetSHA256 == oldScriptHash || entry.LauncherTargetSHA256 != oldScriptHash {
		t.Fatalf("script mutation lost its independent entry digest: %+v %v", fresh, err)
	}
}

func TestWindowsNodeShimRejectsInstallationEscape(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.js")
	if err := os.WriteFile(outside, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "wecom-cli.cmd")
	if err := os.WriteFile(shim, []byte(`@node "%~dp0\..\outside.js" %*`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WindowsNodeShimTarget(shim); err == nil {
		t.Fatal("accepted a shim outside the Windows npm prefix")
	}
	if _, err := snapshotCLIEntries("wecom", root, "windows"); err == nil {
		t.Fatal("published the escaping Windows shim")
	}
}
