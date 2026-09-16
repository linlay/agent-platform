package connectorauth

import (
	"context"
	"encoding/json"
	"fmt"
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
		"versionCheck": map[string]any{"minVersion": "1.2.0", "command": commands("demo --version")},
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pkg = writeCLIPackage(t, root, pkg.ID, pkg.CLI)
	testNodeCLI(t, filepath.Join(npm, "cli.js"), pkg)
	if _, err := m.Prepare(ctx, pkg.ID); err != nil {
		t.Fatal(err)
	}
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
	pkg = writeCLIPackage(t, root, pkg.ID, pkg.CLI)
	write(filepath.Join(npm, "cli.js"), `require('fs').writeFileSync(require('path').join(process.env.DEMO_CLI_CONFIG_DIR,'unexpected-install'),'started')`)
	if _, err := m.cliStatus(ctx, pkg); err == nil {
		t.Fatal("missing native CLI was reported ready")
	}
	if _, err := os.Stat(filepath.Join(state, "config", "unexpected-install")); !os.IsNotExist(err) {
		t.Fatal("status executed an unprepared native installer")
	}
}

func TestCLIAuthStepsPreserveShellDeclarations(t *testing.T) {
	for _, line := range []string{"demo init; other", "demo $(whoami)", "npm install demo", "demo status > file"} {
		for _, field := range []string{"command", "skipIf"} {
			fields := map[string]any{"command": map[string]any{"darwin": "demo init", "linux": "demo init", "win32": "demo init"}, "authUrlDomain": "example.test"}
			fields[field] = map[string]any{"darwin": line, "linux": line, "win32": line}
			pkg := connector.Package{CLI: map[string]any{"auth": []any{fields}}}
			if _, err := cliAuthSteps(pkg, "demo"); err != nil {
				encoded, _ := json.Marshal(fields)
				t.Fatalf("rejected declared step %s", encoded)
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
		{"https://sub.login.example.test/authorize\n", ""},
		{"https://login.example.test:8443/authorize\n", ""},
		{"https://login.example.test@evil.example.test/authorize\n", ""},
	} {
		if got := cliAuthorizationURL(tt.output, "login.example.test"); got != tt.want {
			t.Fatalf("URL extraction: got %q, want %q", got, tt.want)
		}
	}
}

func TestCLIStatusMatchersCombineAndExitZeroDefault(t *testing.T) {
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"] = osCommands("demo login")
	cli["status"] = osCommands("demo status")
	cli["unAuth"] = osCommands("demo logout")
	cli["authUrlDomain"] = "example.test"
	cli["platform"] = map[string]any{"configEnv": "DEMO_CLI_CONFIG_DIR"}
	pkg := writeCLIPackage(t, root, "demo", cli)
	script := filepath.Join(t.TempDir(), "demo.js")
	os.WriteFile(script, []byte(`if(process.argv[2]==='--version')console.log('1.2.0');else console.log('{"authorized":true,"role":"reader"}')`), 0600)
	testNodeCLI(t, script, pkg)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		pattern  string
		expected map[string]any
		want     bool
	}{
		{"", nil, true},
		{"reader", map[string]any{"authorized": true}, true},
		{"admin", map[string]any{"authorized": true}, false},
		{"reader", map[string]any{"authorized": false}, false},
	} {
		pkg.CLI["statusMatch"] = tt.pattern
		if tt.expected == nil {
			delete(pkg.CLI, "statusMatchJson")
		} else {
			pkg.CLI["statusMatchJson"] = tt.expected
		}
		got, err := m.cliStatus(context.Background(), pkg)
		if err != nil || got != tt.want {
			t.Fatalf("%+v: %v %v", tt, got, err)
		}
	}
}

func TestCLISkipIfUnexpectedFailureDoesNotStartAuthorization(t *testing.T) {
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"] = []any{map[string]any{"command": osCommands("demo login"), "skipIf": osCommands("demo check"), "authUrlDomain": "example.test"}}
	cli["status"], cli["unAuth"] = osCommands("demo status"), osCommands("demo logout")
	cli["statusMatch"] = "^authorized$"
	cli["platform"] = map[string]any{"configEnv": "DEMO_CLI_CONFIG_DIR"}
	pkg := writeCLIPackage(t, root, "demo", cli)
	script := filepath.Join(t.TempDir(), "demo.js")
	os.WriteFile(script, []byte(`const fs=require('fs'),path=require('path'),dir=process.env.DEMO_CLI_CONFIG_DIR,c=process.argv[2];if(c==='--version')console.log('1.2.0');else if(c==='status')console.log('no');else if(c==='check')process.exit(2);else fs.writeFileSync(path.join(dir,'unexpected'),'yes')`), 0600)
	testNodeCLI(t, script, pkg)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.loginCLI(context.Background(), pkg, &login{}); err == nil {
		t.Fatal("skipIf exit 2 did not fail")
	}
	dir, _ := pkg.UserStateDir()
	if _, err := os.Stat(filepath.Join(dir, "config", "unexpected")); !os.IsNotExist(err) {
		t.Fatal("authorization executed after prerequisite failed")
	}
}

func TestCLIAuthWithoutWaitingPollsAndTerminatesOwnProcess(t *testing.T) {
	for _, launcherExits := range []bool{false, true} {
		t.Run(fmt.Sprint(launcherExits), func(t *testing.T) {
			root := t.TempDir()
			cli := simpleCLI()
			cli["auth"], cli["status"], cli["unAuth"] = osCommands("demo login"), osCommands("demo status"), osCommands("demo logout")
			cli["authWaitForExit"], cli["authUrlDomain"], cli["statusMatch"] = false, "example.test", "^authorized"
			cli["platform"] = map[string]any{"configEnv": "DEMO_CLI_CONFIG_DIR"}
			pkg := writeCLIPackage(t, root, "demo", cli)
			script := filepath.Join(t.TempDir(), "demo.js")
			body := `const fs=require('fs'),path=require('path'),dir=process.env.DEMO_CLI_CONFIG_DIR,c=process.argv[2];if(c==='--version')console.log('1.2.0');else if(c==='status')console.log(fs.existsSync(path.join(dir,'approved'))?'authorized':'no');else{console.log('https://example.test/login');`
			if !launcherExits {
				body += `setInterval(()=>{},100);setTimeout(()=>fs.writeFileSync(path.join(dir,'unexpected'),'yes'),1800);`
			}
			body += "}"
			os.WriteFile(script, []byte(body), 0600)
			testNodeCLI(t, script, pkg)
			m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if _, err := m.Prepare(ctx, pkg.ID); err != nil {
				t.Fatal(err)
			}
			dir, _ := pkg.UserStateDir()
			done := make(chan error, 1)
			go func() { done <- m.loginCLI(ctx, pkg, &login{}) }()
			time.Sleep(300 * time.Millisecond)
			os.WriteFile(filepath.Join(dir, "config", "approved"), []byte("yes"), 0600)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			time.Sleep(1800 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "config", "unexpected")); !os.IsNotExist(err) {
				t.Fatal("session process survived successful authorization")
			}
		})
	}
}

func TestCLIAuthStepDefaultsAndOverrides(t *testing.T) {
	pkg := connector.Package{CLI: map[string]any{
		"authUrlDomain": "login.example.test", "authWaitForExit": false,
		"auth": []any{
			map[string]any{"command": osCommands("demo configure"), "authWaitForExit": true},
			map[string]any{"command": osCommands("demo login")},
			map[string]any{"command": osCommands("demo verify"), "authUrlDomain": "verify.example.test", "authWaitForExit": true},
		},
	}}
	steps, err := cliAuthSteps(pkg, "demo")
	if err != nil || len(steps) != 3 {
		t.Fatal(steps, err)
	}
	if steps[0].domain != "login.example.test" || !steps[0].waitForExit || steps[1].domain != "login.example.test" || steps[1].waitForExit || steps[2].domain != "verify.example.test" || !steps[2].waitForExit {
		t.Fatalf("step defaults/overrides lost: %+v", steps)
	}
	for _, test := range []struct {
		domain, url string
		want        bool
	}{
		{"localhost", "http://localhost:49152/authorize", true},
		{"127.0.0.1", "http://127.0.0.1:49152/authorize", true},
		{"example.test", "http://example.test/authorize", false},
		{"localhost", "http://localhost.evil.test/authorize", false},
		{"", "https://example.test/authorize", false},
	} {
		got := cliAuthorizationURL(test.url+"\n", test.domain)
		if (got != "") != test.want {
			t.Fatalf("%+v: got %q", test, got)
		}
	}
}

func TestCLIAuthMultiStepLocalSetupAndWaitOverridesExecute(t *testing.T) {
	for _, topWait := range []bool{false, true} {
		t.Run(fmt.Sprint(topWait), func(t *testing.T) {
			root := t.TempDir()
			cli := simpleCLI()
			cli["authWaitForExit"] = topWait
			cli["auth"] = []any{
				map[string]any{"command": osCommands("demo configure"), "authWaitForExit": true},
				map[string]any{"command": osCommands("demo login"), "authWaitForExit": false, "authUrlDomain": "login.example.test"},
			}
			cli["status"], cli["unAuth"] = osCommands("demo status"), osCommands("demo logout")
			cli["statusMatchJson"] = map[string]any{"authorized": true}
			cli["platform"] = map[string]any{"configEnv": "DEMO_CLI_CONFIG_DIR"}
			pkg := writeCLIPackage(t, root, "demo", cli)
			script := filepath.Join(t.TempDir(), "demo.js")
			body := `const fs=require('fs'),path=require('path'),dir=process.env.DEMO_CLI_CONFIG_DIR,p=n=>path.join(dir,n),cmd=process.argv[2];if(cmd==='--version')console.log('1.2.0');else if(cmd==='status')console.log(JSON.stringify({authorized:fs.existsSync(p('login'))}));else if(cmd==='configure')fs.writeFileSync(p('configured'),'yes');else if(cmd==='login'){if(!fs.existsSync(p('configured')))process.exit(8);console.log('https://login.example.test/auth');fs.writeFileSync(p('login'),'yes');setInterval(()=>{},100);}else{fs.rmSync(p('login'),{force:true});}`
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			testNodeCLI(t, script, pkg)
			m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := m.Prepare(ctx, pkg.ID); err != nil {
				t.Fatal(err)
			}
			if err := m.loginCLI(ctx, pkg, &login{}); err != nil {
				t.Fatal("step wait override did not progress", err)
			}
			if err := m.logoutCLI(ctx, pkg); err != nil {
				t.Fatal("unAuth failed", err)
			}
			ok, err := m.cliStatus(ctx, pkg)
			if err != nil || ok {
				t.Fatal("logout retained login", ok, err)
			}
		})
	}
}

func TestCLIProbeFailureDoesNotStartLogin(t *testing.T) {
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"], cli["status"], cli["unAuth"] = osCommands("demo login"), osCommands("demo status"), osCommands("demo logout")
	cli["platform"] = map[string]any{"configEnv": "DEMO_CLI_CONFIG_DIR"}
	pkg := writeCLIPackage(t, root, "demo", cli)
	script := filepath.Join(t.TempDir(), "demo.js")
	body := `const fs=require('fs'),path=require('path'),c=process.argv[2];if(c==='--version')console.log('1.2.0');else if(c==='status')process.exit(2);else fs.writeFileSync(path.join(process.env.DEMO_CLI_CONFIG_DIR,'unexpected-login'),'yes');`
	os.WriteFile(script, []byte(body), 0600)
	testNodeCLI(t, script, pkg)
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.loginCLI(t.Context(), pkg, &login{}); err == nil {
		t.Fatal("failed status treated as missing login")
	}
	private, _ := pkg.UserStateDir()
	if _, err := os.Stat(filepath.Join(private, "config", "unexpected-login")); !os.IsNotExist(err) {
		t.Fatal("login ran after failed probe", err)
	}
}
