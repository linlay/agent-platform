package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

func TestCLILifecyclePhaseEnvironmentAndShellExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process integration; Windows argv contract checked separately")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["staticEnv"] = map[string]any{"JIRA_URL": "https://jira.example.test"}
	pkg := writeCLIPackage(t, root, "demo", cli)
	pkg.AuthMode = connector.AuthToken
	pkg.TokenSchema = json.RawMessage(`{"fields":[{"key":"JIRA_PASSWORD","label":"Password","type":"password","required":true}]}`)
	pkg.CLI["env"] = map[string]any{"CONNECTOR_TEST_SECRET": "${JIRA_PASSWORD}"}
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	credentials, err := connector.CredentialsPath(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePrivateJSON(credentials, map[string]string{"JIRA_PASSWORD": "private-test-value"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_ACCESS_TOKEN", "must-not-inherit")
	t.Setenv("JIRA_PASSWORD", "must-not-inherit")
	for _, phase := range []CLIPhase{CLIPhaseInit, CLIPhaseVersionCheck, CLIPhaseAuth, CLIPhaseSkipIf, CLIPhaseStatus, CLIPhaseUnAuth, CLIPhaseBusiness} {
		env, err := m.cliPhaseEnvironment(context.Background(), pkg, phase)
		if err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
		if hostenv.Value(env, "AP_ACCESS_TOKEN") != "" || hostenv.Value(env, "JIRA_PASSWORD") != "" {
			t.Fatalf("phase %s inherited unrelated credentials", phase)
		}
		if hostenv.Value(env, "JIRA_URL") != "https://jira.example.test" {
			t.Fatalf("phase %s did not receive staticEnv", phase)
		}
		wantDynamic := phase == CLIPhaseStatus || phase == CLIPhaseBusiness
		if (hostenv.Value(env, "CONNECTOR_TEST_SECRET") != "") != wantDynamic {
			t.Fatalf("phase %s received wrong credential scope", phase)
		}
		if hostenv.Value(env, "CONNECTOR_ARCH") != runtime.GOARCH || hostenv.Value(env, "CONNECTOR_BIN_DIR") == "" {
			t.Fatalf("phase %s omitted installation variables", phase)
		}
		if !strings.HasPrefix(hostenv.Value(env, "HOME"), pkg.PersistentRoot()+string(filepath.Separator)) {
			t.Fatalf("phase %s did not use private HOME", phase)
		}
		line := `set -eu; test -f connector.json; test "$JIRA_URL" = 'https://jira.example.test'; test -n "$CONNECTOR_BIN_DIR"; test -z "${AP_ACCESS_TOKEN-}"`
		if wantDynamic {
			line += `; test -n "$CONNECTOR_TEST_SECRET"`
		} else {
			line += `; test -z "${CONNECTOR_TEST_SECRET-}"`
		}
		cmd, err := m.cliLifecycleCommand(context.Background(), pkg, phase, line)
		if err != nil || cmd.Path != "/bin/sh" || !reflect.DeepEqual(cmd.Args[1:2], []string{"-c"}) {
			t.Fatalf("phase %s shell command: %v %v", phase, cmd, err)
		}
		if err := cmd.Run(); err != nil {
			t.Fatalf("phase %s execution: %v", phase, err)
		}
	}
}

func TestCLISemVerAndECMAScriptPatterns(t *testing.T) {
	for _, test := range []struct {
		output, minimum string
		want            bool
	}{
		{"v1.2.0", "1.2.0", true},
		{"1.2.0+build.4", "1.2.0", true},
		{"1.2.0-beta.9", "1.2.0", false},
		{"1.2.0-beta.10", "1.2.0-beta.9", true},
		{"1.2.0-beta.1", "1.2.0-beta", true},
		{"1.2.0-01", "1.2.0", false},
		{"01.2.0", "1.2.0", false},
		{"999999999999999999999.0.0", "2.0.0", true},
		{`{"version":"1.2.0"}`, "1.2.0", false},
		{"program 1.2.0", "1.2.0", false},
	} {
		if got := versionAtLeast(test.output, test.minimum); got != test.want {
			t.Errorf("versionAtLeast(%q, %q) = %v", test.output, test.minimum, got)
		}
	}
	re, err := cliPattern(`(?<=version:)([0-9]+\.[0-9]+\.[0-9]+)(?=\s)`)
	if err != nil {
		t.Fatal(err)
	}
	match, err := re.FindStringMatch("version:1.2.3 \n")
	if err != nil || match == nil || match.GroupByNumber(1).String() != "1.2.3" {
		t.Fatalf("ECMAScript lookaround extraction: %v %v", match, err)
	}
	re, err = cliPattern(`^(\w+)\s+\1$`)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := re.MatchString("signed signed"); err != nil || !matched {
		t.Fatalf("ECMAScript backreference: %v %v", matched, err)
	}
	// A pathological package pattern is bounded and reports a check failure,
	// instead of hanging authorization or interpreting timeout as logged out.
	re, err = cliPattern(`^(a+)+$`)
	if err != nil {
		t.Fatal(err)
	}
	re.MatchTimeout = 20 * time.Millisecond
	started := time.Now()
	if _, err := re.MatchString(strings.Repeat("a", 1000) + "!"); err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("ECMAScript timeout was not enforced: %v", err)
	}
}

func TestCLIVersionJSONRequiresExplicitExtraction(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cli.js")
	if err := os.WriteFile(script, []byte(`console.log(JSON.stringify({version:"1.2.0"}))`), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	root := t.TempDir()
	cli := simpleCLI()
	writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if state, err := m.Prepare(context.Background(), "demo"); err == nil || state.Status != "failed" {
		t.Fatalf("JSON version without versionPattern was accepted: %+v %v", state, err)
	}
	cli["versionCheck"].(map[string]any)["versionPattern"] = `(?<="version":")([0-9]+\.[0-9]+\.[0-9]+)(?=")`
	writeCLIPackage(t, root, "demo", cli)
	if state, err := m.Prepare(context.Background(), "demo"); err != nil || state.Status != "ready" {
		t.Fatalf("explicit JSON extraction failed: %+v %v", state, err)
	}
}

func TestCLIStatusMatchesWholeObjectAndAllConditions(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cli.js")
	if err := os.WriteFile(script, []byte(`if(process.argv[2]==='--version') console.log('1.2.0'); else { console.error('stderr is not part of status'); console.log(JSON.stringify({ok:true,data:{identity:'user'},authenticated:true})); }`), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	root := t.TempDir()
	cli := simpleCLI()
	cli["status"] = osCommands("demo status")
	cli["statusMatchJson"] = map[string]any{"ok": true, "data": map[string]any{"identity": "user"}}
	cli["statusMatch"] = `(?<="authenticated":)true(?=})`
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.cliStatus(context.Background(), pkg); err != nil || !ok {
		t.Fatalf("whole-object subset plus pattern: %v %v", ok, err)
	}
	pkg.CLI["statusMatch"] = "absent"
	if ok, err := m.cliStatus(context.Background(), pkg); err != nil || ok {
		t.Fatalf("regex was not required alongside JSON: %v %v", ok, err)
	}
	delete(pkg.CLI, "statusMatch")
	pkg.CLI["statusMatchJson"] = map[string]any{"identity": "user"}
	if ok, err := m.cliStatus(context.Background(), pkg); err != nil || ok {
		t.Fatalf("standard status unexpectedly unwrapped legacy envelope: %v %v", ok, err)
	}
	delete(pkg.CLI, "statusMatchJson")
	if ok, err := m.cliStatus(context.Background(), pkg); err != nil || !ok {
		t.Fatalf("exit zero without match predicates failed: %v %v", ok, err)
	}
}

func TestCLINPMInitPinsVersionsAndUsesPrivatePrefix(t *testing.T) {
	for _, line := range []string{"npm install --global demo@latest", "npm install -g demo@^1.2.0", "npm install demo@1.2.0", "npm --prefix /tmp/global install -g demo@1.2.0", "NPM_CONFIG_PREFIX=/tmp/global npm install -g demo@1.2.0"} {
		if err := validateNPMInit(line); err == nil {
			t.Errorf("accepted unsafe npm init %q", line)
		}
	}
	if err := validateNPMInit("npm install --global --loglevel error @example/demo@1.2.0"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pkg := writeCLIPackage(t, root, "demo", simpleCLI())
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	t.Setenv("npm_config_prefix", filepath.Join(t.TempDir(), "user-global"))
	env, err := m.cliPhaseEnvironment(context.Background(), pkg, CLIPhaseInit)
	if err != nil {
		t.Fatal(err)
	}
	bin := hostenv.Value(env, "CONNECTOR_BIN_DIR")
	wantPrefix := filepath.Dir(bin)
	if runtime.GOOS == "windows" {
		wantPrefix = bin
	}
	if hostenv.Value(env, "npm_config_prefix") != wantPrefix || hostenv.Value(env, "NPM_CONFIG_PREFIX") != wantPrefix {
		t.Fatal("npm global install did not use the platform version directory")
	}
}

func TestCLIOSSelectionAndWindowsShellContract(t *testing.T) {
	commands := map[string]any{"darwin": "mac-cli --version", "linux": "linux-cli --version", "win32": "win-cli.exe --version"}
	for _, test := range []struct{ os, want string }{{"darwin", "mac-cli --version"}, {"linux", "linux-cli --version"}, {"windows", "win-cli.exe --version"}} {
		if got, err := cliOSCommandFor(commands, test.os); err != nil || got != test.want {
			t.Fatalf("OS %s: %q %v", test.os, got, err)
		}
	}
	if _, err := cliOSCommandFor(map[string]any{"darwin": "cli --version"}, "windows"); err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_CAPABILITY") {
		t.Fatal("missing OS did not report unsupported capability", err)
	}
	dir := t.TempDir()
	cmdExe := filepath.Join(dir, "cmd.exe")
	if err := os.WriteFile(cmdExe, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	line := `test.exe && echo %CONNECTOR_ARCH%`
	cmd, err := cliShellCommand(context.Background(), dir, []string{"PATH=" + dir}, line, "windows")
	if err != nil || !reflect.DeepEqual(cmd.Args, []string{cmdExe, "/d", "/s", "/c", line}) {
		t.Fatalf("Windows lifecycle shell argv: %v %v", cmd, err)
	}
}

func TestCLIAuthorizationURLRequiresExactHost(t *testing.T) {
	for _, test := range []struct{ text, want string }{
		{"https://login.example.test/authorize\n", "https://login.example.test/authorize"},
		{"https://child.login.example.test/authorize\n", ""},
		{"http://login.example.test/authorize\n", ""},
		{"http://127.0.0.1:4000/authorize\n", "http://127.0.0.1:4000/authorize"},
	} {
		if got := cliAuthorizationURL(test.text, "login.example.test"); got != test.want {
			t.Fatalf("URL %q became %q", test.text, got)
		}
	}
}

func TestCLIAuthStopsOnPrerequisiteFailureAndPollsWithoutWaitingForExit(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cli.js")
	body := `const fs=require('fs'),path=require('path'),p=n=>path.join(process.env.XDG_CONFIG_HOME,n),cmd=process.argv[2];
if(cmd==='--version') console.log('1.2.0');
else if(cmd==='skip') process.exit(fs.existsSync(p('permit'))?1:2);
else if(cmd==='status'){if(fs.existsSync(p('status-failed'))) process.exit(2); console.log(JSON.stringify({authenticated:fs.existsSync(p('login'))}));}
else if(cmd==='login'){fs.writeFileSync(p('auth-called'),'yes');setTimeout(()=>fs.writeFileSync(p('login'),'yes'),25);setInterval(()=>{},1000);}
else if(cmd==='logout') fs.rmSync(p('login'),{force:true});`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"] = []any{map[string]any{"command": osCommands("demo login"), "skipIf": osCommands("demo skip"), "authWaitForExit": false}}
	cli["status"], cli["unAuth"] = osCommands("demo status"), osCommands("demo logout")
	cli["statusMatchJson"] = map[string]any{"authenticated": true}
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	state, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.loginCLI(ctx, pkg, &login{}); err == nil {
		t.Fatal("skipIf exit 2 did not stop authorization")
	}
	if _, err := os.Stat(filepath.Join(state, "config", "auth-called")); !os.IsNotExist(err) {
		t.Fatal("authorization ran after prerequisite failure")
	}
	if err := os.WriteFile(filepath.Join(state, "config", "permit"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "config", "status-failed"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.loginCLI(ctx, pkg, &login{}); err == nil {
		t.Fatal("initial status failure was treated as logged out")
	}
	if _, err := os.Stat(filepath.Join(state, "config", "auth-called")); !os.IsNotExist(err) {
		t.Fatal("authorization ran after status execution failure")
	}
	if err := os.Remove(filepath.Join(state, "config", "status-failed")); err != nil {
		t.Fatal(err)
	}
	// login never exits by itself. authWaitForExit=false polls status, then
	// terminates/reaps the dedicated process before declaring success.
	if err := m.loginCLI(ctx, pkg, &login{}); err != nil {
		t.Fatalf("authWaitForExit=false did not finish on valid status: %v", err)
	}
	if ok, err := m.cliStatus(ctx, pkg); err != nil || !ok {
		t.Fatalf("polled login was not usable: %v %v", ok, err)
	}
}

func TestCLITokenCandidateReusesVersionPrivateInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload integration")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["init"] = osCommands(`cp ./bin/demo-payload "$CONNECTOR_BIN_DIR/demo"; chmod 755 "$CONNECTOR_BIN_DIR/demo"`)
	cli["env"] = map[string]any{"API_KEY": "${API_KEY}"}
	cli["status"] = osCommands("demo status")
	cli["statusMatchJson"] = map[string]any{"authenticated": true}
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	payload := "#!/bin/sh\nif test \"$1\" = --version; then printf '1.2.0\\n'; exit 0; fi\nif test \"$API_KEY\" = good-test; then printf '{\"authenticated\":true}\\n'; else printf '{\"authenticated\":false}\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "demo-payload"), []byte(payload), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := connector.Manifest{ID: "demo", Name: "demo", Version: "1.0.0", Type: "cli", AuthMode: connector.AuthToken, TokenSchema: json.RawMessage(`{"fields":[{"key":"API_KEY","label":"API Key","type":"password","required":true}]}`)}
	for name, value := range map[string]any{"connector.json": manifest, "cli.json": cli} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources := connector.Sources{ExternalRoot: root}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), sources, nil)
	prepared, err := m.Prepare(context.Background(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := CLIInstallationBinDir(pkg)
	if err != nil || prepared.Executable != filepath.Join(bin, "demo") {
		t.Fatalf("payload was not installed privately: %+v %v", prepared, err)
	}
	if session, err := m.SetToken(context.Background(), pkg.ID, map[string]string{"API_KEY": "good-test"}); err != nil || session.Status != "authorized" {
		t.Fatalf("candidate did not reuse active program installation: %+v %v", session, err)
	}
	binding, err := CLIEnvironment(pkg)
	if err != nil {
		t.Fatal(err)
	}
	values, err := ResolveEnvironment(context.Background(), binding, "")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH")}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	cmd, err := ExecutionCommand(context.Background(), pkg, []string{"status"}, env)
	canonicalEntry, canonicalErr := filepath.EvalSymlinks(prepared.Executable)
	if err != nil || canonicalErr != nil || cmd.Path != canonicalEntry {
		t.Fatalf("business call did not use prepared version-private executable: %v %v", cmd, err)
	}
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), `"authenticated":true`) {
		t.Fatalf("business invocation after candidate commit: %v %v", string(output), err)
	}
}
