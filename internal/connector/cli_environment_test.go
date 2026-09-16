package connector

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestCLIEnvironmentSeparatesUserStateAndVersionedPrograms(t *testing.T) {
	p := Package{Manifest: Manifest{ID: "demo", Version: "1.2.3"}, StateRoot: t.TempDir(), Owner: "alice", CLI: map[string]any{
		"platform": map[string]any{"configEnv": "DEMO_CONFIG_DIR"},
		"env":      map[string]any{"DYNAMIC_SECRET": "must not inject into lifecycle"},
	}}
	alice, err := p.CLIConfigEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	p.Owner = "bob"
	bob, err := p.CLIConfigEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "DEMO_CONFIG_DIR"} {
		if alice[key] == "" || alice[key] == bob[key] {
			t.Fatalf("%s not isolated", key)
		}
	}
	if alice["CONNECTOR_BIN_DIR"] != bob["CONNECTOR_BIN_DIR"] {
		t.Fatal("program installation depends on account")
	}
	if bob["CONNECTOR_ARCH"] != runtime.GOARCH {
		t.Fatal("architecture not exposed")
	}
	if _, ok := bob["DYNAMIC_SECRET"]; ok {
		t.Fatal("dynamic env injected into lifecycle")
	}
	if runtime.GOOS == "windows" && (bob["USERPROFILE"] != bob["HOME"] || bob["APPDATA"] != bob["XDG_CONFIG_HOME"]) {
		t.Fatal("Windows credential roots are not isolated")
	}
	p.Version = "2.0.0"
	updated, err := p.CLIConfigEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if updated["CONNECTOR_BIN_DIR"] == bob["CONNECTOR_BIN_DIR"] {
		t.Fatal("program installation not versioned")
	}
	if updated["HOME"] != bob["HOME"] {
		t.Fatal("version upgrade discarded account root")
	}
	dirs, err := p.CLIBinDirs()
	if err != nil {
		t.Fatal(err)
	}
	if dirs[0] != updated["CONNECTOR_BIN_DIR"] || filepath.Base(dirs[1]) != ".bin" {
		t.Fatalf("unexpected bin lookup %v", dirs)
	}
}
