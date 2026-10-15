package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
	"agent-platform/internal/shellenv"
	"mvdan.cc/sh/v3/shell"
)

// cliSettings holds what Platform derives from cli.json. Command is the managed
// executable named by versionCheck; it is empty when versionCheck is absent or
// starts with an interpreter or a path, and then the package has no pinned
// entry for direct invocation.
type cliSettings struct {
	Command string
}

// Interpreters and launchers run lifecycle scripts; they never become the
// connector's pinned executable.
var cliLaunchers = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "env": true, "node": true, "npx": true, "npm": true,
	"python": true, "python3": true, "powershell": true, "pwsh": true, "cmd": true, "set": true,
}

func cliSettingsFor(pkg connector.Package) (cliSettings, error) {
	var s cliSettings
	if config, ok := pkg.CLI["versionCheck"].(map[string]any); ok {
		line, err := cliLine(config["command"], false)
		if err != nil {
			return s, fmt.Errorf("versionCheck.command: %w", err)
		}
		if words, err := shell.Fields(line, func(string) string { return "" }); err == nil && len(words) > 0 {
			command := strings.TrimSuffix(words[0], ".cmd")
			if connector.ValidID(command) && !cliLaunchers[strings.ToLower(command)] {
				s.Command = command
			}
		}
		minimum, _ := config["minVersion"].(string)
		if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(minimum) {
			return s, fmt.Errorf("CLI requires versionCheck.minVersion")
		}
		if pattern, ok := config["versionPattern"].(string); ok && pattern != "" {
			re, err := regexp.Compile(pattern)
			if err != nil || re.NumSubexp() == 0 {
				return s, fmt.Errorf("CLI versionPattern requires a capture group")
			}
		}
	}
	if _, err := pkg.CLIConfigEnvironment(); err != nil {
		return s, err
	}
	if pkg.CLI["init"] != nil {
		if _, err := cliLine(pkg.CLI["init"], false); err != nil {
			return s, fmt.Errorf("init: %w", err)
		}
	}
	if pkg.ManagedCLI() {
		if _, err := cliAuthSteps(pkg); err != nil {
			return s, err
		}
		if _, err := cliLine(pkg.CLI["unAuth"], true); err != nil {
			return s, fmt.Errorf("unAuth: %w", err)
		}
	}
	if pkg.CLI["status"] != nil {
		if _, err := cliLine(pkg.CLI["status"], false); err != nil {
			return s, fmt.Errorf("status: %w", err)
		}
		if raw, exists := pkg.CLI["statusMatch"]; exists {
			pattern, _ := raw.(string)
			if _, err := regexp.Compile(pattern); err != nil || pattern == "" {
				return s, fmt.Errorf("invalid statusMatch")
			}
		}
		if raw, exists := pkg.CLI["statusMatchJson"]; exists {
			if fields, ok := raw.(map[string]any); !ok || len(fields) == 0 {
				return s, fmt.Errorf("statusMatchJson must be a nonempty object")
			}
		}
	}
	return s, nil
}

type cliAuthStep struct {
	command, skipIf string
	domain          string
	waitForExit     bool
	suppressBrowser bool
	deviceFlow      *cliDeviceFlow
}

// cliDeviceFlow reads an RFC 8628 verification link and user code from output.
type cliDeviceFlow struct {
	uri, code *regexp.Regexp
	expiresIn time.Duration
}

// cliAuthSteps resolves each step; a step's own fields override the top level.
func cliAuthSteps(pkg connector.Package) ([]cliAuthStep, error) {
	declarations, multi := pkg.CLI["auth"].([]any)
	if !multi {
		declarations = []any{map[string]any{"command": pkg.CLI["auth"]}}
	}
	if len(declarations) == 0 || len(declarations) > 16 {
		return nil, fmt.Errorf("CLI auth requires 1 to 16 steps")
	}
	var steps []cliAuthStep
	for _, declaration := range declarations {
		fields, ok := declaration.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("CLI auth step must be an object")
		}
		inherit := func(key string) any {
			if value, exists := fields[key]; exists {
				return value
			}
			return pkg.CLI[key]
		}
		step := cliAuthStep{}
		var err error
		if step.command, err = cliLine(fields["command"], false); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		if _, exists := fields["skipIf"]; exists {
			if step.skipIf, err = cliLine(fields["skipIf"], false); err != nil {
				return nil, fmt.Errorf("auth skipIf: %w", err)
			}
		}
		step.domain, _ = inherit("authUrlDomain").(string)
		step.waitForExit, _ = inherit("authWaitForExit").(bool)
		step.suppressBrowser, _ = inherit("authSuppressBrowser").(bool)
		if flow, ok := inherit("authDeviceFlow").(map[string]any); ok {
			step.deviceFlow = &cliDeviceFlow{expiresIn: 5 * time.Minute}
			pattern, _ := flow["uriPattern"].(string)
			if step.deviceFlow.uri, err = regexp.Compile(pattern); err != nil || pattern == "" {
				return nil, fmt.Errorf("invalid authDeviceFlow.uriPattern")
			}
			if pattern, ok := flow["codePattern"].(string); ok {
				if step.deviceFlow.code, err = regexp.Compile(pattern); err != nil {
					return nil, fmt.Errorf("invalid authDeviceFlow.codePattern")
				}
			}
			if seconds, ok := flow["defaultExpiresInSeconds"].(float64); ok {
				step.deviceFlow.expiresIn = time.Duration(seconds) * time.Second
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// extract prefers the first capture group, falling back to the whole match.
func (f *cliDeviceFlow) extract(text string) (uri, code string) {
	first := func(re *regexp.Regexp) string {
		match := re.FindStringSubmatch(text)
		if len(match) > 1 && match[1] != "" {
			return strings.TrimSpace(match[1])
		}
		if len(match) > 0 {
			return strings.TrimSpace(match[0])
		}
		return ""
	}
	uri = first(f.uri)
	if u, err := url.Parse(uri); uri == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", ""
	}
	if f.code != nil {
		code = first(f.code)
	}
	return uri, code
}

func cliOS() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// cliLine returns this OS's shell line from an OS command map. An optional
// command may be an empty string, meaning there is nothing to run.
func cliLine(raw any, optional bool) (string, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	var commands map[string]string
	if raw == nil || json.Unmarshal(data, &commands) != nil {
		return "", fmt.Errorf("OS command map is required")
	}
	for key := range commands {
		if key != "darwin" && key != "linux" && key != "win32" {
			return "", fmt.Errorf("unknown OS key %q", key)
		}
	}
	line, ok := commands[cliOS()]
	if !ok {
		return "", fmt.Errorf("command for %s is required", cliOS())
	}
	if strings.ContainsRune(line, 0) {
		return "", fmt.Errorf("command contains NUL")
	}
	if strings.TrimSpace(line) == "" && !optional {
		return "", fmt.Errorf("command for %s is required", cliOS())
	}
	return line, nil
}

// cliEnvironment is the base of every CLI phase: the same inherited host
// variables as Host Bash, the package's bin/, the active installation's bin
// and the public cli.json env. HOME stays the user's own.
func (m *Manager) cliEnvironment(pkg connector.Package) ([]string, error) {
	return m.cliEnvironmentWith(pkg, connector.ActiveInstallBin(pkg.InstallRoot, pkg.ID))
}

func (m *Manager) cliEnvironmentWith(pkg connector.Package, installBin string) ([]string, error) {
	env := hostenv.WithSystemPaths(shellenv.InheritedEnvironment(os.Environ(), m.inheritEnv...))
	env = builtins.EnsureBinInEnv(env)
	var bins []string
	for _, dir := range []string{pkg.BinDir, installBin} {
		if dir != "" {
			bins = append(bins, dir)
		}
	}
	env = connector.WithPath(env, bins)
	values, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	return env, nil
}

// lifecycleEnvironment is the environment of status, login and logout. A token
// candidate under validation runs with only OS essentials, its own staged
// credentials and private HOME/XDG directories.
func (m *Manager) lifecycleEnvironment(ctx context.Context, pkg connector.Package) ([]string, error) {
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	if pkg.AuthMode != connector.AuthToken || pkg.PersistentRoot() == m.sources.PersistentRoot() {
		return env, nil
	}
	binding, err := CLIEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	values, err := ResolveEnvironment(ctx, binding, "")
	if err != nil {
		return nil, err
	}
	clean := []string{}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "SystemRoot", "WINDIR", "ComSpec", "LANG", "LC_ALL":
			clean = append(clean, entry)
		}
	}
	env = clean
	config, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return nil, err
	}
	for key, value := range config {
		env = hostenv.Set(env, key, value)
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		return nil, err
	}
	for key, name := range map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp"} {
		env = hostenv.Set(env, key, filepath.Join(dir, name))
	}
	return env, nil
}

// lifecycleCommand runs one cli.json line through the OS shell, as WorkBuddy
// does: installing a package trusts its lifecycle commands. Business calls
// still run the pinned executable with direct argv, never through a shell.
func lifecycleCommand(ctx context.Context, pkg connector.Package, line string, env []string) (*exec.Cmd, func(), error) {
	cleanup := func() {}
	shellName, args := "bash", []string{"-c", line}
	if runtime.GOOS == "windows" {
		// A script file keeps cmd.exe from re-parsing the line as /c arguments.
		dir, err := pkg.ConnectorStateDir()
		if err != nil {
			return nil, nil, err
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, nil, err
		}
		script, err := os.CreateTemp(dir, ".lifecycle-*.cmd")
		if err != nil {
			return nil, nil, err
		}
		name := script.Name()
		cleanup = func() { _ = os.Remove(name) }
		_, err = script.WriteString("@echo off\r\n" + line + "\r\n")
		if closeErr := script.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		shellName, args = "cmd.exe", []string{"/d", "/s", "/c", "\"\"" + name + "\"\""}
	}
	executable, err := hostenv.LookPath(shellName, env)
	if err != nil && shellName == "bash" {
		executable, err = hostenv.LookPath("sh", env)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = pkg.Dir
	cmd.Env = env
	configureProcess(cmd)
	configureCommandLine(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, cleanup, nil
}

// runLifecycle runs a line to completion and returns its combined output.
func runLifecycle(ctx context.Context, pkg connector.Package, line string, env []string) (string, *exec.Cmd, error) {
	cmd, cleanup, err := lifecycleCommand(ctx, pkg, line, env)
	if err != nil {
		return "", nil, err
	}
	defer cleanup()
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return out.String(), cmd, err
}

func cliExecutable(ctx context.Context, entry string, args []string, dir string, env []string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, entry, args...)
	cmd.Dir = dir
	cmd.Env = env
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

// cliRegistries lists the npm registries init tries in order; "" keeps the
// user's own npm configuration.
func cliRegistries(cli map[string]any) []string {
	if value, ok := cli["npmRegistry"].(string); ok {
		return []string{value}
	}
	if list, ok := cli["npmRegistries"].([]any); ok {
		var registries []string
		for _, raw := range list {
			if value, ok := raw.(string); ok {
				registries = append(registries, value)
			}
		}
		return registries
	}
	return []string{""}
}

// npmNetworkError reports failures worth retrying with the next registry.
func npmNetworkError(output string) bool {
	for _, marker := range []string{"FETCH_ERROR", "ETIMEDOUT", "ECONNREFUSED", "ENOTFOUND", "EAI_AGAIN", "ECONNRESET",
		"UNABLE_TO_VERIFY_LEAF_SIGNATURE", "SELF_SIGNED_CERT_IN_CHAIN", "CERT_HAS_EXPIRED", "socket hang up"} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

// prepareCLI obeys init verbatim. Only an explicit preparation may invoke it.
// npm global installs land in a private prefix keyed by package version and
// fingerprint; the returned prefix is empty when init did not run.
// The caller releases the returned lease once the attempt is active.
func (m *Manager) prepareCLI(ctx context.Context, pkg connector.Package, fingerprint string) (prefix string, release func(), err error) {
	release = func() {}
	dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return "", release, err
	}
	if err = os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		return "", release, err
	}
	if pkg.BinDir == "" && pkg.CLI["init"] != nil {
		var lease func()
		if prefix, lease, err = connector.NewInstallPrefix(m.sources.InstallRoot(), pkg.ID, pkg.Version, fingerprint); err != nil {
			return "", release, err
		}
		release = lease
		// A failed attempt's directory is never active; remove it.
		attempt := prefix
		defer func() {
			if err != nil {
				_ = os.RemoveAll(attempt)
				lease()
			}
		}()
	}
	// The new installation is searched first; the active one stays in use
	// until this preparation succeeds.
	env, err := m.cliEnvironmentWith(pkg, connector.InstallBin(prefix))
	if err != nil {
		return "", release, err
	}
	if requirement, ok := pkg.CLI["runtime"].(map[string]any); ok {
		required, _ := requirement["version"].(string)
		major, err := strconv.Atoi(strings.TrimPrefix(required, ">="))
		if requirement["type"] != "node" || err != nil {
			return "", release, fmt.Errorf("unsupported CLI runtime requirement")
		}
		node, err := hostenv.LookPath("node", env)
		if err != nil {
			return "", release, fmt.Errorf("Node.js >=%d is required", major)
		}
		cmd, err := cliExecutable(ctx, node, []string{"--version"}, pkg.Dir, env)
		if err != nil {
			return "", release, err
		}
		var out boundedOutput
		cmd.Stdout = &out
		if err = cmd.Run(); err != nil || !versionAtLeast(out.String(), strconv.Itoa(major)+".0.0") {
			return "", release, fmt.Errorf("Node.js >=%d is required", major)
		}
	}
	if pkg.BinDir == "" && pkg.CLI["init"] != nil {
		line, err := cliLine(pkg.CLI["init"], false)
		if err != nil {
			return "", release, err
		}
		registries := cliRegistries(pkg.CLI)
		cache := filepath.Join(m.sources.InstallRoot(), ".npm-cache")
		for i, registry := range registries {
			runEnv := hostenv.Set(env, "npm_config_prefix", prefix)
			runEnv = hostenv.Set(runEnv, "npm_config_cache", cache)
			if registry != "" {
				runEnv = hostenv.Set(runEnv, "npm_config_registry", registry)
			}
			output, cmd, err := runLifecycle(ctx, pkg, line, runEnv)
			hostenv.Refresh()
			if err == nil {
				break
			}
			if cmd == nil {
				return "", release, err
			}
			if ctx.Err() == nil && i+1 < len(registries) && npmNetworkError(output) {
				continue
			}
			return "", release, &CLIExecutionError{Stage: "init", ExitCode: exitCode(cmd), Diagnostic: diagnosticExcerpt(output), Cause: err}
		}
	}
	config, ok := pkg.CLI["versionCheck"].(map[string]any)
	if !ok {
		return prefix, release, nil
	}
	line, err := cliLine(config["command"], false)
	if err != nil {
		return "", release, err
	}
	versionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, cmd, err := runLifecycle(versionCtx, pkg, line, env)
	if err != nil {
		if cmd == nil {
			return "", release, err
		}
		return "", release, &CLIExecutionError{Stage: "versionCheck", ExitCode: exitCode(cmd), Diagnostic: diagnosticExcerpt(output), Cause: err}
	}
	minimum, _ := config["minVersion"].(string)
	if pattern, _ := config["versionPattern"].(string); pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", release, err
		}
		match := re.FindStringSubmatch(output)
		if len(match) < 2 {
			return "", release, fmt.Errorf("CLI versionPattern did not match")
		}
		output = match[1]
	}
	if !versionAtLeast(output, minimum) {
		return "", release, fmt.Errorf("CLI version is below required %s", minimum)
	}
	return prefix, release, nil
}

func versionAtLeast(output, minimum string) bool {
	re := regexp.MustCompile(`([0-9]+)\.([0-9]+)\.([0-9]+)\b`)
	a, b := re.FindStringSubmatch(output), re.FindStringSubmatch(minimum)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		av, _ := strconv.Atoi(a[i])
		bv, _ := strconv.Atoi(b[i])
		if av != bv {
			return av > bv
		}
	}
	return true
}

type boundedOutput struct {
	mu      sync.Mutex
	data    []byte
	onWrite func(string)
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 65536 {
		b.data = b.data[len(b.data)-65536:]
	}
	if b.onWrite != nil {
		b.onWrite(string(b.data))
	}
	return len(p), nil
}
func (b *boundedOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

// cliStatus reports whether the CLI is signed in. A nonzero exit means signed
// out; a shell that cannot start the program is an installation failure.
func (m *Manager) cliStatus(ctx context.Context, pkg connector.Package) (bool, error) {
	if _, err := cliSettingsFor(pkg); err != nil {
		return false, err
	}
	// Status must not start an installer's implicit binary download. Only the
	// explicit preparation/version-check phase may run an unprepared wrapper.
	if err := m.requirePrepared(pkg); err != nil {
		return false, err
	}
	line, err := cliLine(pkg.CLI["status"], false)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env, err := m.lifecycleEnvironment(ctx, pkg)
	if err != nil {
		return false, err
	}
	cmd, cleanup, err := lifecycleCommand(ctx, pkg, line, env)
	if err != nil {
		return false, err
	}
	defer cleanup()
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return false, fmt.Errorf("CLI authorization status timed out")
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return false, fmt.Errorf("CLI authorization status command failed")
		}
		if code := exit.ExitCode(); code == 126 || code == 127 || code == 9009 {
			return false, fmt.Errorf("CLI authorization status command could not start")
		}
		return false, nil
	}
	return cliStatusMatches(pkg.CLI, stdout.String(), stderr.String()), nil
}

// cliStatusMatches accepts a successful status when either declared matcher
// matches; without matchers the exit status alone decides.
func cliStatusMatches(cli map[string]any, stdout, stderr string) bool {
	pattern, hasPattern := cli["statusMatch"].(string)
	expected, hasJSON := cli["statusMatchJson"].(map[string]any)
	if !hasPattern && !hasJSON {
		return true
	}
	if hasJSON {
		var actual map[string]any
		if json.Unmarshal([]byte(stdout), &actual) == nil && actual["ok"] != false {
			if actual["ok"] == true {
				if data, ok := actual["data"].(map[string]any); ok {
					actual = data
				}
			}
			if jsonSubset(actual, expected) {
				return true
			}
		}
	}
	if hasPattern {
		re, err := regexp.Compile(pattern)
		return err == nil && re.MatchString(stdout+stderr)
	}
	return false
}

func jsonSubset(actual, expected map[string]any) bool {
	for key, want := range expected {
		got, exists := actual[key]
		if !exists {
			return false
		}
		if child, ok := want.(map[string]any); ok {
			object, ok := got.(map[string]any)
			if !ok || !jsonSubset(object, child) {
				return false
			}
			continue
		}
		// A string expectation also matches a scalar that prints the same,
		// as WorkBuddy compares String(value).
		if text, ok := want.(string); ok {
			switch value := got.(type) {
			case bool:
				got = strconv.FormatBool(value)
			case float64:
				got = strconv.FormatFloat(value, 'f', -1, 64)
			}
			if got == text {
				continue
			}
			return false
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			return false
		}
	}
	return true
}

func (m *Manager) loginCLI(ctx context.Context, pkg connector.Package, l *login) error {
	if _, err := cliSettingsFor(pkg); err != nil {
		return err
	}
	if err := m.requirePrepared(pkg); err != nil {
		return err
	}
	if ok, err := m.cliStatus(ctx, pkg); err == nil && ok {
		return nil
	}
	steps, err := cliAuthSteps(pkg)
	if err != nil {
		return err
	}
	env, err := m.lifecycleEnvironment(ctx, pkg)
	if err != nil {
		return err
	}
	poll := false
	for _, step := range steps {
		if step.skipIf != "" {
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			// Configuration output may contain app secrets: discard it entirely.
			_, _, err := runLifecycle(checkCtx, pkg, step.skipIf, env)
			checkExpired := checkCtx.Err()
			cancel()
			if checkExpired != nil {
				return fmt.Errorf("CLI authorization prerequisite check timed out")
			}
			if err == nil {
				continue
			}
		}
		m.mu.Lock()
		l.URL, l.UserCode, l.UserCodeExpiresAt, l.Status, l.Message = "", "", nil, "preparing", "Preparing authorization step"
		m.mu.Unlock()
		exited, err := m.runCLIAuthStep(ctx, pkg, env, l, step)
		if err != nil {
			return err
		}
		poll = poll || !exited
	}
	if poll {
		return m.awaitCLIAuthorization(ctx, pkg)
	}
	ok, err := m.cliStatus(ctx, pkg)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("CLI exited without an authorized login state")
	}
	return nil
}

// awaitCLIAuthorization polls status after a step that did not wait for exit.
func (m *Manager) awaitCLIAuthorization(ctx context.Context, pkg connector.Package) error {
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("CLI login was not completed in time; start login again")
		case <-ticker.C:
			if ok, err := m.cliStatus(ctx, pkg); err == nil && ok {
				return nil
			}
		}
	}
}

// runCLIAuthStep reports whether the step ran to exit. A step that does not
// wait for exit ends once it prints the authorization link, and the caller
// then polls status. Device flows always wait: the CLI polls the token itself.
func (m *Manager) runCLIAuthStep(ctx context.Context, pkg connector.Package, env []string, l *login, step cliAuthStep) (bool, error) {
	stepCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd, cleanup, err := lifecycleCommand(stepCtx, pkg, step.command, env)
	if err != nil {
		return false, err
	}
	defer cleanup()
	waitForExit := step.waitForExit || step.deviceFlow != nil
	found := make(chan struct{})
	var once sync.Once
	output := boundedOutput{onWrite: func(text string) {
		if step.deviceFlow != nil {
			if uri, code := step.deviceFlow.extract(text); uri != "" {
				once.Do(func() {
					m.setCLIAuthorization(l, uri, code, step.deviceFlow.expiresIn, step.suppressBrowser)
					close(found)
				})
			}
			return
		}
		if raw := cliAuthorizationURL(text, step.domain); raw != "" {
			once.Do(func() {
				m.setCLIAuthorization(l, raw, "", 0, step.suppressBrowser)
				close(found)
			})
		}
	}}
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("CLI login could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if !waitForExit {
		select {
		case <-found:
			stop()
			<-done
			return false, nil
		case err = <-done:
		}
	} else {
		err = <-done
	}
	if err != nil {
		return true, fmt.Errorf("CLI login failed or expired; start login again")
	}
	return waitForExit, nil
}

// setCLIAuthorization publishes a link and an optional device code. When the
// CLI opens the browser itself, the link is withheld so it does not open twice.
func (m *Manager) setCLIAuthorization(l *login, link, code string, expiresIn time.Duration, cliOpensBrowser bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.Status = "pending"
	l.URL = link
	l.Message = "Open the authorization link and complete login"
	if cliOpensBrowser {
		l.URL = ""
		l.Message = "Complete login in the browser opened by the CLI"
	}
	l.UserCode = code
	l.UserCodeExpiresAt = nil
	if code != "" {
		if !cliOpensBrowser {
			l.Message = "Open the authorization link and confirm the code"
		}
		expires := time.Now().Add(expiresIn).UTC()
		l.UserCodeExpiresAt = &expires
	}
}

func cliAuthorizationURL(text, domain string) string {
	allowed := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
			return false
		}
		return domain == "" || u.Hostname() == domain || strings.HasSuffix(u.Hostname(), "."+domain)
	}
	// JSON encoders can escape '&' as & or '/' as \/. Decode only the
	// JSON string, preserving the URL itself and its query encoding verbatim.
	for _, literal := range regexp.MustCompile(`"(?:\\.|[^"\\])*"`).FindAllString(text, -1) {
		var raw string
		if json.Unmarshal([]byte(literal), &raw) == nil && allowed(raw) {
			return raw
		}
	}
	for _, span := range regexp.MustCompile(`https://[^\s<>"'\\]+`).FindAllStringIndex(text, -1) {
		// An output chunk may end mid-URL or mid-JSON escape. Wait for the
		// terminator instead of exposing a truncated authorization link.
		if span[1] == len(text) || text[span[1]] == '\\' {
			continue
		}
		raw := strings.TrimRight(text[span[0]:span[1]], ").,\r\n")
		if allowed(raw) {
			return raw
		}
	}
	return ""
}

// logoutCLI runs unAuth. An empty unAuth declares no CLI sign-out.
func (m *Manager) logoutCLI(ctx context.Context, pkg connector.Package) error {
	if _, err := cliSettingsFor(pkg); err != nil {
		return err
	}
	line, err := cliLine(pkg.CLI["unAuth"], true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) == "" {
		return nil
	}
	if err := m.requirePrepared(pkg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	env, err := m.lifecycleEnvironment(ctx, pkg)
	if err != nil {
		return err
	}
	if _, _, err := runLifecycle(ctx, pkg, line, env); err != nil {
		return fmt.Errorf("CLI logout failed")
	}
	return nil
}
