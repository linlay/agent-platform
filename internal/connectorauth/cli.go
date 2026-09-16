package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"mvdan.cc/sh/v3/shell"
)

type cliSettings struct {
	NPMPackage  string `json:"npmPackage"`
	NPMVersion  string `json:"npmVersion"`
	Entry       string `json:"entry"`
	NativeEntry string `json:"nativeEntry,omitempty"`
	Command     string `json:"command"`
	ConfigEnv   string `json:"configEnv"`
	LogoutMode  string `json:"logoutMode,omitempty"`
}

func cliSettingsFor(pkg connector.Package) (cliSettings, error) {
	var s cliSettings
	data, _ := json.Marshal(pkg.CLI["platform"])
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	config, _ := pkg.CLI["versionCheck"].(map[string]any)
	line, err := cliOSCommand(config["command"])
	if err != nil {
		return s, fmt.Errorf("versionCheck.command: %w", err)
	}
	words, err := shell.Fields(line, func(string) string { return "" })
	if err != nil || len(words) == 0 {
		return s, fmt.Errorf("versionCheck.command is required")
	}
	command := strings.TrimSuffix(words[0], ".cmd")
	if !connector.ValidID(command) {
		return s, fmt.Errorf("versionCheck must invoke a command name")
	}
	if s.Command != "" && s.Command != command {
		return s, fmt.Errorf("platform.command must match versionCheck.command")
	}
	s.Command = command
	if _, err := pkg.CLIPrivateEnvironment(); err != nil {
		return s, err
	}
	if s.LogoutMode == "delete-config" && s.ConfigEnv == "" {
		return s, fmt.Errorf("delete-config requires configEnv")
	}
	if s.LogoutMode != "" && s.LogoutMode != "delete-config" {
		return s, fmt.Errorf("invalid CLI logout mode")
	}
	if pkg.ManagedCLI() {
		if _, err := cliAuthSteps(pkg, s.Command); err != nil {
			return s, err
		}
		if _, err := cliArgs(pkg, "status", s.Command); err != nil {
			return s, err
		}
		if s.LogoutMode == "" {
			if _, err := cliArgs(pkg, "unAuth", s.Command); err != nil {
				return s, err
			}
		}
		pattern, _ := pkg.CLI["statusMatch"].(string)
		if expected, exists := pkg.CLI["statusMatchJson"]; exists {
			fields, ok := expected.(map[string]any)
			if !ok || len(fields) == 0 {
				return s, fmt.Errorf("CLI requires a nonempty statusMatchJson object")
			}
		}
		if _, err := compileCLIRegexp(pattern); err != nil {
			return s, fmt.Errorf("invalid statusMatch")
		}
	}
	if _, err := cliVersionArgs(pkg, s.Command); err != nil {
		return s, err
	}
	if config, ok := pkg.CLI["versionCheck"].(map[string]any); ok {
		minimum, _ := config["minVersion"].(string)
		if !validCLISemVer(minimum) {
			return s, fmt.Errorf("CLI requires versionCheck.minVersion")
		}
		if pattern, ok := config["versionPattern"].(string); ok && pattern != "" {
			re, err := compileCLIRegexp(pattern)
			if err != nil || len(re.GetGroupNumbers()) < 2 {
				return s, fmt.Errorf("CLI versionPattern requires a capture group")
			}
		}
	} else {
		return s, fmt.Errorf("CLI requires versionCheck")
	}
	return s, nil
}

type cliAuthStep struct {
	args, skipArgs []string
	domain         string
	waitForExit    bool
}

// Ordered authorization steps still invoke only the pinned executable.
func cliAuthSteps(pkg connector.Package, command string) ([]cliAuthStep, error) {
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
		step := cliAuthStep{waitForExit: true}
		// Step options override the top-level defaults. Local prerequisite steps
		// need no browser domain; they may run but cannot expose arbitrary URLs.
		option := func(key string) (any, bool) {
			if value, exists := fields[key]; exists {
				return value, true
			}
			value, exists := pkg.CLI[key]
			return value, exists
		}
		if raw, exists := option("authUrlDomain"); exists {
			domain, ok := raw.(string)
			if !ok || !regexp.MustCompile(`(?i)^[a-z0-9]+(?:[.-][a-z0-9]+)*$`).MatchString(domain) {
				return nil, fmt.Errorf("CLI auth step requires a valid authUrlDomain")
			}
			step.domain = strings.ToLower(domain)
		}
		if raw, exists := option("authWaitForExit"); exists {
			var ok bool
			step.waitForExit, ok = raw.(bool)
			if !ok {
				return nil, fmt.Errorf("CLI authWaitForExit must be boolean")
			}
		}
		if raw, exists := option("authSuppressBrowser"); exists {
			if _, ok := raw.(bool); !ok {
				return nil, fmt.Errorf("CLI authSuppressBrowser must be boolean")
			}
		}
		var err error
		step.args, err = cliArgs(connector.Package{CLI: fields}, "command", command)
		if err != nil {
			return nil, err
		}
		if _, exists := fields["skipIf"]; exists {
			step.skipArgs, err = cliArgs(connector.Package{CLI: fields}, "skipIf", command)
			if err != nil {
				return nil, err
			}
			if len(step.skipArgs) == 0 {
				return nil, fmt.Errorf("CLI skipIf requires explicit arguments")
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func cliVersionArgs(pkg connector.Package, command string) ([]string, error) {
	config, _ := pkg.CLI["versionCheck"].(map[string]any)
	if _, ok := config["command"]; !ok {
		return []string{command + " --version"}, nil
	}
	return cliArgs(connector.Package{CLI: config}, "command", command)
}

// Lifecycle commands are OS-specific shell programs declared by the package.
// They are not reparsed into argv or silently rewritten by Platform.
func cliArgs(pkg connector.Package, key, command string) ([]string, error) {
	line, err := cliOSCommand(pkg.CLI[key])
	if err != nil {
		return nil, fmt.Errorf("CLI %s: %w", key, err)
	}
	return []string{line}, nil
}

func cliOSCommand(raw any) (string, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	var commands map[string]string
	if json.Unmarshal(data, &commands) != nil {
		return "", fmt.Errorf("OS command map is required")
	}
	key := runtime.GOOS
	if key == "windows" {
		key = "win32"
	}
	line, ok := commands[key]
	if !ok || strings.TrimSpace(line) == "" {
		return "", fmt.Errorf("command for %s is required", key)
	}
	return line, nil
}

func (m *Manager) cliEnvironment(pkg connector.Package) ([]string, error) {
	env := builtins.EnsureBinInEnv(os.Environ())
	dirs, err := pkg.CLIBinDirs()
	if err != nil {
		return nil, err
	}
	env = connector.WithPath(env, dirs)
	values, err := pkg.CLIPrivateEnvironment()
	if err != nil {
		return nil, err
	}
	if static, ok := pkg.CLI["staticEnv"].(map[string]any); ok {
		for key, value := range static {
			if text, ok := value.(string); ok {
				env = hostenv.Set(env, key, text)
			}
		}
	}
	// Private roots cannot be overridden by package static environment values.
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	return env, nil
}

func (m *Manager) cliCommand(ctx context.Context, pkg connector.Package, s cliSettings, args ...string) (*exec.Cmd, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("CLI requires one declared OS command")
	}
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	executable, shellArgs := "/bin/sh", []string{"-c", args[0]}
	if runtime.GOOS == "windows" {
		executable, err = hostenv.LookPath("cmd.exe", env)
		if err != nil {
			return nil, err
		}
		shellArgs = []string{"/d", "/s", "/c", args[0]}
	}
	cmd := exec.CommandContext(ctx, executable, shellArgs...)
	cmd.Dir, cmd.Env = pkg.Dir, env
	configureProcess(cmd)
	configureCommandLine(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

func cliExecutable(ctx context.Context, entry string, args []string, dir string, env []string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" && (strings.EqualFold(filepath.Ext(entry), ".cmd") || strings.EqualFold(filepath.Ext(entry), ".bat")) {
		// Only lifecycle arguments parsed as literals reach this adapter. Avoid cmd
		// expansion of percent, quotes and metacharacters in these arguments.
		words := append([]string{entry}, args...)
		for i, word := range words {
			if strings.ContainsAny(word, "\"%\r\n&|<>^!") {
				return nil, fmt.Errorf("unsupported Windows CLI argument")
			}
			words[i] = "\"" + word + "\""
		}
		shellPath, err := hostenv.LookPath("cmd.exe", env)
		if err != nil {
			return nil, err
		}
		entry = shellPath
		args = []string{"/d", "/s", "/c", "\"" + strings.Join(words, " ") + "\""}
	}
	cmd := exec.CommandContext(ctx, entry, args...)
	cmd.Dir = dir
	cmd.Env = env
	configureProcess(cmd)
	configureCommandLine(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

// prepareCLI obeys init verbatim. Only an explicit preparation may invoke it.
func (m *Manager) prepareCLI(ctx context.Context, pkg connector.Package, s cliSettings) error {
	installDir, err := pkg.InstallDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(installDir, "setup")
	for _, child := range []string{"home", "config", "cache", "data", "state", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0700); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(installDir, "bin"), 0700); err != nil {
		return err
	}
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return err
	}
	setup := map[string]string{"HOME": filepath.Join(dir, "home"), "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "XDG_DATA_HOME": filepath.Join(dir, "data"), "XDG_STATE_HOME": filepath.Join(dir, "state"), "TMPDIR": filepath.Join(dir, "tmp")}
	if runtime.GOOS == "windows" {
		setup["USERPROFILE"] = setup["HOME"]
		setup["APPDATA"] = setup["XDG_CONFIG_HOME"]
		setup["LOCALAPPDATA"] = setup["XDG_DATA_HOME"]
		setup["TEMP"] = setup["TMPDIR"]
		setup["TMP"] = setup["TMPDIR"]
	}
	if s.ConfigEnv != "" {
		setup[s.ConfigEnv] = filepath.Join(dir, "config")
	}
	for key, value := range setup {
		env = hostenv.Set(env, key, value)
	}
	if requirement, ok := pkg.CLI["runtime"].(map[string]any); ok {
		required, _ := requirement["version"].(string)
		major, err := strconv.Atoi(strings.TrimPrefix(required, ">="))
		if requirement["type"] != "node" || err != nil {
			return fmt.Errorf("unsupported CLI runtime requirement")
		}
		node, err := hostenv.LookPath("node", env)
		if err != nil {
			return fmt.Errorf("Node.js >=%d is required", major)
		}
		cmd, err := cliExecutable(ctx, node, []string{"--version"}, pkg.Dir, env)
		if err != nil {
			return err
		}
		var out boundedOutput
		cmd.Stdout = &out
		if err = cmd.Run(); err != nil || !versionAtLeast(out.String(), strconv.Itoa(major)+".0.0") {
			return fmt.Errorf("Node.js >=%d is required", major)
		}
	}
	// Version verification precedes installation. Repeated prepare never reruns an
	// already satisfied installer or downgrades a newer pinned installation.
	if err = m.checkCLIVersion(ctx, pkg, s, env); err == nil {
		return nil
	}
	if pkg.CLI["init"] == nil {
		return fmt.Errorf("unsupported_capability: no compatible private CLI and no init")
	}
	if pkg.CLI["init"] != nil {
		line, err := cliOSCommand(pkg.CLI["init"])
		if err != nil {
			return err
		}
		if err := validatePrivateCLIInit(line); err != nil {
			return err
		}
		initEnv, envErr := npmInitEnvironment(pkg, env, line)
		if envErr != nil {
			return envErr
		}
		shellName, args := "/bin/sh", []string{"-c", line}
		if runtime.GOOS == "windows" {
			shellName = "cmd.exe"
			args = []string{"/d", "/s", "/c", line}
		}
		executable, err := hostenv.LookPath(shellName, env)
		if err != nil {
			return err
		}
		if runtime.GOOS == "windows" {
			script, err := os.CreateTemp(dir, ".init-*.cmd")
			if err != nil {
				return err
			}
			defer os.Remove(script.Name())
			if _, err = script.WriteString(line); err != nil {
				script.Close()
				return err
			}
			if err = script.Close(); err != nil {
				return err
			}
			args = []string{"/d", "/s", "/c", "\"\"" + script.Name() + "\"\""}
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir = pkg.Dir
		cmd.Env = initEnv
		configureProcess(cmd)
		configureCommandLine(cmd)
		cmd.WaitDelay = 2 * time.Second
		var out boundedOutput
		cmd.Stdout = &out
		cmd.Stderr = &out
		err = cmd.Run()
		hostenv.Refresh()
		if err != nil {
			return &CLIExecutionError{Stage: "init", ExitCode: exitCode(cmd), Diagnostic: out.String(), Cause: err}
		}
	}
	if err := exposePrivateNPMEntry(pkg, s.Command); err != nil {
		return err
	}
	return m.checkCLIVersion(ctx, pkg, s, env)
}

func (m *Manager) checkCLIVersion(ctx context.Context, pkg connector.Package, s cliSettings, env []string) error {
	if _, err := m.cliPrivateEntry(pkg, s.Command); err != nil {
		return err
	}
	args, err := cliVersionArgs(pkg, s.Command)
	if err != nil {
		return err
	}
	cmd, err := m.cliCommand(ctx, pkg, s, args...)
	if err != nil {
		return err
	}
	cmd.Env = env
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return &CLIExecutionError{Stage: "versionCheck", ExitCode: exitCode(cmd), Diagnostic: out.String(), Cause: err}
	}
	config, _ := pkg.CLI["versionCheck"].(map[string]any)
	minimum, _ := config["minVersion"].(string)
	output := out.String()
	if pattern, _ := config["versionPattern"].(string); pattern != "" {
		re, err := compileCLIRegexp(pattern)
		if err != nil {
			return err
		}
		match, matchErr := re.FindStringMatch(output)
		if matchErr != nil || match == nil || match.GroupByNumber(1) == nil {
			return fmt.Errorf("CLI versionPattern did not match")
		}
		output = match.GroupByNumber(1).String()
	}
	if !versionAtLeast(output, minimum) {
		return fmt.Errorf("CLI version is below required %s", minimum)
	}
	return nil
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

func (m *Manager) cliStatus(ctx context.Context, pkg connector.Package) (bool, error) {
	s, err := cliSettingsFor(pkg)
	if err != nil {
		return false, err
	}
	// Status must not start an installer's implicit binary download. Only the
	// explicit preparation/version-check phase may run an unprepared wrapper.
	if err := m.requireNativeCLI(pkg, s); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args, err := cliArgs(pkg, "status", s.Command)
	if err != nil {
		return false, err
	}
	cmd, err := m.cliCommand(ctx, pkg, s, args...)
	if err != nil {
		return false, err
	}
	values, err := m.credentialEnvironment(ctx, pkg)
	if err != nil {
		return false, err
	}
	for key, value := range values {
		cmd.Env = hostenv.Set(cmd.Env, key, value)
	}
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if pkg.CLI["statusMatchJson"] != nil {
		cmd.Stderr = io.Discard
	}
	err = cmd.Run()
	if ctx.Err() != nil {
		return false, fmt.Errorf("CLI authorization status timed out")
	}
	if err != nil {
		return false, fmt.Errorf("CLI authorization status command failed")
	}
	if expected, ok := pkg.CLI["statusMatchJson"].(map[string]any); ok {
		var actual map[string]any
		if err := json.Unmarshal([]byte(output.String()), &actual); err != nil {
			return false, fmt.Errorf("CLI authorization status was not valid JSON")
		}
		if actual["ok"] == false {
			return false, nil
		}
		if actual["ok"] == true {
			if data, ok := actual["data"].(map[string]any); ok {
				actual = data
			}
		}
		if !jsonSubset(actual, expected) {
			return false, nil
		}
	}
	pattern, _ := pkg.CLI["statusMatch"].(string)
	re, err := compileCLIRegexp(pattern)
	if err != nil {
		return false, fmt.Errorf("invalid statusMatch")
	}
	matched, err := re.MatchString(output.String())
	if err != nil {
		return false, fmt.Errorf("statusMatch exceeded execution limit")
	}
	return matched, nil
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
		} else {
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if string(a) != string(b) {
				return false
			}
		}
	}
	return true
}

func (m *Manager) loginCLI(ctx context.Context, pkg connector.Package, l *login) error {
	s, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	if err := m.requirePrepared(pkg); err != nil {
		return err
	}
	privateDir, err := pkg.UserStateDir()
	if err != nil {
		return err
	}
	for _, child := range []string{"home", "config", "cache", "data", "state"} {
		if err := os.MkdirAll(filepath.Join(privateDir, child), 0700); err != nil {
			return err
		}
	}
	if ok, err := m.cliStatus(ctx, pkg); err != nil {
		// A failed probe is not evidence that credentials are missing. Never
		// replace a valid login because a status command or network failed.
		return err
	} else if ok {
		return nil
	}
	steps, err := cliAuthSteps(pkg, s.Command)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if len(step.skipArgs) > 0 {
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			check, err := m.cliCommand(checkCtx, pkg, s, step.skipArgs...)
			if err != nil {
				cancel()
				return err
			}
			// Configuration output may contain app secrets: discard it entirely.
			err = check.Run()
			checkExpired := checkCtx.Err()
			cancel()
			if checkExpired != nil {
				return fmt.Errorf("CLI authorization prerequisite check timed out")
			}
			if err == nil {
				continue
			}
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 1 {
				return fmt.Errorf("CLI authorization prerequisite check failed")
			}
		}
		m.mu.Lock()
		l.URL, l.Status, l.Message = "", "preparing", "Preparing authorization step"
		m.mu.Unlock()
		if err := m.runCLIAuthStep(ctx, pkg, s, l, step); err != nil {
			return err
		}
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

func (m *Manager) runCLIAuthStep(ctx context.Context, pkg connector.Package, s cliSettings, l *login, step cliAuthStep) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := m.cliCommand(childCtx, pkg, s, step.args...)
	if err != nil {
		return err
	}
	output := boundedOutput{onWrite: func(text string) {
		if raw := cliAuthorizationURL(text, step.domain); raw != "" {
			m.setURL(l, raw)
		}
	}}
	cmd.Stdout = &output
	cmd.Stderr = &output
	if step.waitForExit {
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("CLI login failed or expired; start login again")
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("CLI login could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Cancel only this session's process tree and always reap it. Never kill a
	// command by executable name: other owners may be authorizing concurrently.
	defer func() {
		cancel()
		if done != nil {
			<-done
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			done = nil
			if err != nil {
				return fmt.Errorf("CLI login failed or expired; start login again")
			}
			// Some browser launchers exit before authorization completes.
		case <-ticker.C:
			if authorized, err := m.cliStatus(ctx, pkg); err == nil && authorized {
				return nil
			}
		}
	}
}

func cliAuthorizationURL(text, domain string) string {
	allowed := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || domain == "" || u.User != nil || !strings.EqualFold(u.Hostname(), domain) {
			return false
		}
		if u.Scheme == "https" {
			return u.Port() == "" || u.Port() == "443"
		}
		return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || strings.EqualFold(u.Hostname(), "localhost") || u.Hostname() == "::1")
	}
	// JSON encoders can escape '&' as \u0026 or '/' as \/. Decode only the
	// JSON string, preserving the URL itself and its query encoding verbatim.
	for _, literal := range regexp.MustCompile(`"(?:\\.|[^"\\])*"`).FindAllString(text, -1) {
		var raw string
		if json.Unmarshal([]byte(literal), &raw) == nil && allowed(raw) {
			return raw
		}
	}
	for _, span := range regexp.MustCompile(`https?://[^\s<>"'\\]+`).FindAllStringIndex(text, -1) {
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

func (m *Manager) logoutCLI(ctx context.Context, pkg connector.Package) error {
	s, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	if s.LogoutMode == "delete-config" {
		dir, err := pkg.UserStateDir()
		if err != nil {
			return err
		}
		// Only Platform's own per-connector credential subtree is removable.
		return os.RemoveAll(filepath.Join(dir, "config"))
	}
	if err := m.requireNativeCLI(pkg, s); err != nil {
		return err
	}
	args, err := cliArgs(pkg, "unAuth", s.Command)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := m.cliCommand(ctx, pkg, s, args...)
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CLI logout failed")
	}
	return nil
}

func (m *Manager) requireNativeCLI(pkg connector.Package, s cliSettings) error {
	return m.requirePrepared(pkg)
}
