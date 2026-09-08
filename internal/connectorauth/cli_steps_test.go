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

func TestCLIOrderedAuthStepsAndJSONStatus(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required for CLI subprocess integration")
	}
	commands := func(line string) map[string]any {
		return map[string]any{"darwin": line, "linux": line, "win32": line}
	}
	root := t.TempDir()
	pkg := connector.Package{Manifest: connector.Manifest{ID: "steps"}, CLI: map[string]any{
		"platform":     map[string]any{"npmPackage": "demo-cli", "npmVersion": "1.2.0", "entry": "cli.js", "command": "demo", "configEnv": "DEMO_CLI_CONFIG_DIR", "logoutMode": "delete-config"},
		"versionCheck": map[string]any{"minVersion": "1.2.0"},
		"auth": []any{
			map[string]any{"command": commands("demo init"), "skipIf": commands("demo configured"), "authUrlDomain": "setup.example.test"},
			map[string]any{"command": commands("demo login"), "authUrlDomain": "login.example.test"},
		},
		"status": commands("demo status"), "unAuth": commands(""),
		"statusMatchJson": map[string]any{"identity": "user"},
	}}
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	state, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	npm := filepath.Join(state, "npm", "node_modules", "demo-cli")
	if err := os.MkdirAll(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(npm, "package.json"), `{"name":"demo-cli","version":"1.2.0"}`)
	write(filepath.Join(npm, "cli.js"), `
const fs=require('fs'),path=require('path'),dir=process.env.DEMO_CLI_CONFIG_DIR;
const p=name=>path.join(dir,name),exists=name=>fs.existsSync(p(name)),cmd=process.argv[2];
if(cmd==='--version') console.log('1.2.0');
else if(cmd==='configured'){console.log('private configuration must be discarded');process.exit(exists('configured')?0:1)}
else if(cmd==='status'){console.error('diagnostic: not JSON');console.log(exists('status')?fs.readFileSync(p('status'),'utf8'):JSON.stringify({ok:true,data:{identity:exists('login')?'user':'bot'}}))}
else {
  if(cmd==='init' && exists('configured')) process.exit(9);
  if(cmd==='login' && !exists('configured')) process.exit(8);
  console.log('https://evil.example.test/not-allowed');
  console.log(cmd==='init'?'https://setup.example.test/authorize':'https://login.example.test/authorize');
  const timer=setInterval(()=>{if(exists('approve-'+cmd)){clearInterval(timer);fs.writeFileSync(p(cmd==='init'?'configured':'login'),'done')}},10);
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	l := &login{}
	done := make(chan error, 1)
	go func() { done <- m.loginCLI(ctx, pkg, l) }()
	waitURL := func(want string) {
		t.Helper()
		for {
			m.mu.Lock()
			got := l.URL
			m.mu.Unlock()
			if got == want {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("login ended before URL %s: %v", want, err)
			case <-ctx.Done():
				t.Fatalf("missing URL %s", want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitURL("https://setup.example.test/authorize")
	write(filepath.Join(state, "config", "approve-init"), "yes")
	waitURL("https://login.example.test/authorize")
	write(filepath.Join(state, "config", "approve-login"), "yes")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Removing only user login must skip the completed setup step.
	if err := os.Remove(filepath.Join(state, "config", "login")); err != nil {
		t.Fatal(err)
	}
	if err := m.loginCLI(ctx, pkg, l); err != nil {
		t.Fatalf("skipIf did not preserve completed setup: %v", err)
	}
	for _, tt := range []struct {
		status string
		want   bool
	}{
		{`{"identity":"user"}`, true},
		{`{"ok":true,"data":{"identity":"user"}}`, true},
		{`{"ok":true,"data":{"identity":"bot"}}`, false},
		{`{"ok":false,"data":{"identity":"user"}}`, false},
		{`{"nested":{"identity":"user"}}`, false},
	} {
		write(filepath.Join(state, "config", "status"), tt.status)
		ok, err := m.cliStatus(ctx, pkg)
		if err != nil || ok != tt.want {
			t.Fatalf("status %s: got %v, %v", tt.status, ok, err)
		}
	}
	// A version wrapper with a missing native payload must not be started by
	// read-only status; it could otherwise download a binary implicitly.
	pkg.CLI["platform"].(map[string]any)["nativeEntry"] = "bin/native-demo"
	write(filepath.Join(npm, "cli.js"), `require('fs').writeFileSync(require('path').join(process.env.DEMO_CLI_CONFIG_DIR,'unexpected-install'),'started')`)
	if _, err := m.cliStatus(ctx, pkg); err == nil {
		t.Fatal("missing native CLI was reported ready")
	}
	if _, err := os.Stat(filepath.Join(state, "config", "unexpected-install")); !os.IsNotExist(err) {
		t.Fatal("status executed an unprepared native installer")
	}
}

func TestCLIAuthStepsRejectUnsafeCommands(t *testing.T) {
	for _, line := range []string{"demo init; other", "demo $(whoami)", "npm install demo", "demo status > file"} {
		for _, field := range []string{"command", "skipIf"} {
			fields := map[string]any{"command": map[string]any{"darwin": "demo init", "linux": "demo init", "win32": "demo init"}, "authUrlDomain": "example.test"}
			fields[field] = map[string]any{"darwin": line, "linux": line, "win32": line}
			pkg := connector.Package{CLI: map[string]any{"auth": []any{fields}}}
			if _, err := cliAuthSteps(pkg, "demo"); err == nil {
				encoded, _ := json.Marshal(fields)
				t.Fatalf("accepted unsafe step %s", encoded)
			}
		}
	}
}

func TestCLIAuthorizationURLPreservesJSONEscapesAndWaitsForChunks(t *testing.T) {
	for _, tt := range []struct{ output, want string }{
		{`{"url":"https:\/\/login.example.test/authorize?a=1\u0026b=%2F"}`, "https://login.example.test/authorize?a=1&b=%2F"},
		{"https://login.example.test/authorize?a=1&b=%2F\n", "https://login.example.test/authorize?a=1&b=%2F"},
		{`{"url":"https://login.example.test/authorize?a=1\u00`, ""},
		{"https://login.example.test/authorize?a=par", ""},
		{"https://evil-login.example.test/authorize\n", ""},
		{"https://login.example.test@evil.example.test/authorize\n", ""},
	} {
		if got := cliAuthorizationURL(tt.output, "login.example.test"); got != tt.want {
			t.Fatalf("URL extraction: got %q, want %q", got, tt.want)
		}
	}
}
