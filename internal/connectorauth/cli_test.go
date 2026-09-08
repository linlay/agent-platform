package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func TestManagedCLILoginStatusIsolationAndCancel(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required for CLI subprocess integration")
	}
	root := t.TempDir()
	id := "demo"
	dir := filepath.Join(root, id)
	os.MkdirAll(dir, 0o755)
	manifest := connector.Manifest{ID: id, Name: id, Version: "1.0.0", Type: "cli", AuthMode: "cli"}
	settings := cliSettings{NPMPackage: "demo-cli", NPMVersion: "1.2.0", Entry: "cli.js", Command: "demo", ConfigEnv: "DEMO_CLI_CONFIG_DIR", LogoutMode: "delete-config"}
	cli := map[string]any{"platform": settings, "versionCheck": map[string]any{"minVersion": "1.2.0"}, "statusMatch": `(?m)^authorized\s*$`, "authUrlDomain": "example.test"}
	for key, cmd := range map[string]string{"auth": "demo login", "status": "demo status", "unAuth": ""} {
		cli[key] = map[string]string{"darwin": cmd, "linux": cmd, "win32": cmd}
	}
	for name, value := range map[string]any{"connector.json": manifest, "cli.json": cli} {
		data, _ := json.Marshal(value)
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	state, _ := StateDir((connector.Sources{ExternalRoot: root}).PersistentRoot(), id)
	npm := filepath.Join(state, "npm", "node_modules", "demo-cli")
	os.MkdirAll(npm, 0o755)
	os.WriteFile(filepath.Join(npm, "package.json"), []byte(`{"name":"demo-cli","version":"1.2.0"}`), 0o644)
	script := `const fs=require('fs'),path=require('path');const dir=process.env.DEMO_CLI_CONFIG_DIR;const args=process.argv.slice(2);if(args[0]==='--version'){console.log('v1.2.0');process.exit(0)}if(args[0]==='status'){console.log(fs.existsSync(path.join(dir,'login'))?'authorized':'unauthorized');process.exit(0)}console.log('https://evil-example.test/login');console.log('https://example.test/authorize?state=test');setTimeout(()=>{fs.mkdirSync(dir,{recursive:true});fs.writeFileSync(path.join(dir,'login'),'private');},500);`
	os.WriteFile(filepath.Join(npm, "cli.js"), []byte(script), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	s := waitStatus(t, m, id, "pending")
	if s.URL != "https://example.test/authorize?state=test" {
		t.Fatal("untrusted authorization URL")
	}
	waitStatus(t, m, id, "authorized")
	if _, err := os.Stat(filepath.Join(state, "config", "login")); err != nil {
		t.Fatal("login did not use isolated config")
	}
	if err := m.Logout(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "config")); !os.IsNotExist(err) {
		t.Fatal("logout retained credentials")
	}
	if _, err := os.Stat(npm); err != nil {
		t.Fatal("logout deleted executable")
	}
	// A canceled subprocess must not complete later and resurrect credentials.
	os.WriteFile(filepath.Join(npm, "cli.js"), []byte(`const fs=require('fs'),path=require('path');if(process.argv[2]==='--version'){console.log('1.2.0')}else if(process.argv[2]==='status'){console.log('unauthorized')}else{console.log('https://example.test/pending');setTimeout(()=>fs.writeFileSync(path.join(process.env.DEMO_CLI_CONFIG_DIR,'login'),'unexpected'),1000)}`), 0o644)
	if _, err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, "pending")
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(state, "config", "login")); !os.IsNotExist(err) {
		t.Fatal("canceled process resurrected login")
	}
}

func TestLifecycleRejectsShellOperationsAndVersionComparison(t *testing.T) {
	for _, cmd := range []string{"demo status; touch /tmp/x", "demo $(whoami)", "demo status > x", "A=B demo status", "demo status &", "other status"} {
		pkg := connector.Package{CLI: map[string]any{"status": map[string]any{"darwin": cmd, "linux": cmd, "win32": cmd}}}
		if _, err := cliArgs(pkg, "status", "demo"); err == nil {
			t.Fatalf("accepted %q", cmd)
		}
	}
	if !versionAtLeast("v1.0.16", "1.0.16") || versionAtLeast("1.0.9", "1.0.16") {
		t.Fatal("incorrect version check")
	}
}
