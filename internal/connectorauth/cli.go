package connectorauth

import (
	"context"
	"encoding/json"
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
	"mvdan.cc/sh/v3/syntax"
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
	if _, err := pkg.CLIConfigEnvironment(); err != nil {
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
			if !ok || len(fields) == 0 || pattern != "" {
				return s, fmt.Errorf("CLI requires a nonempty statusMatchJson object or statusMatch")
			}
		} else if pattern == "" {
			return s, fmt.Errorf("CLI requires statusMatch or statusMatchJson")
		} else if _, err := regexp.Compile(pattern); err != nil {
			return s, fmt.Errorf("invalid statusMatch")
		}
	}
	if _, err := cliVersionArgs(pkg, s.Command); err != nil {
		return s, err
	}
	if config, ok := pkg.CLI["versionCheck"].(map[string]any); ok {
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
	} else {
		return s, fmt.Errorf("CLI requires versionCheck")
	}
	return s, nil
}

type cliAuthStep struct {
	args, skipArgs []string
	domain         string
}

// Ordered authorization steps still invoke only the pinned executable.
func cliAuthSteps(pkg connector.Package, command string) ([]cliAuthStep, error) {
	declarations, multi := pkg.CLI["auth"].([]any)
	if !multi {
		declarations = []any{map[string]any{"command": pkg.CLI["auth"], "authUrlDomain": pkg.CLI["authUrlDomain"]}}
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
		step := cliAuthStep{}
		step.domain, _ = fields["authUrlDomain"].(string)
		if !regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`).MatchString(step.domain) {
			return nil, fmt.Errorf("CLI auth step requires a valid authUrlDomain")
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
		return []string{"--version"}, nil
	}
	return cliArgs(connector.Package{CLI: config}, "command", command)
}

func cliArgs(pkg connector.Package, key, command string) ([]string, error) {
	commands, ok := pkg.CLI[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CLI %s must provide OS commands", key)
	}
	osKey := runtime.GOOS
	if osKey == "windows" {
		osKey = "win32"
	}
	line, _ := commands[osKey].(string)
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, fmt.Errorf("CLI %s must be one command", key)
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(stmt.Redirs) > 0 || stmt.Background || stmt.Negated {
		return nil, fmt.Errorf("CLI lifecycle commands cannot contain shell operations")
	}
	valid := true
	syntax.Walk(call, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp, *syntax.ExtGlob:
			valid = false
		}
		return valid
	})
	if !valid {
		return nil, fmt.Errorf("CLI lifecycle commands cannot contain expansion")
	}
	args, err := shell.Fields(line, func(string) string { return "" })
	if err != nil || len(args) == 0 || strings.TrimSuffix(args[0], ".cmd") != command {
		return nil, fmt.Errorf("CLI %s must invoke its managed executable", key)
	}
	return args[1:], nil
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
	if pkg.BinDir != "" {
		env = connector.WithPath(env, []string{pkg.BinDir})
	}
	values, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	return env, nil
}

func (m *Manager) cliCommand(ctx context.Context, pkg connector.Package, s cliSettings, args ...string) (*exec.Cmd, error) {
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	lookup := env
	if pkg.BinDir != "" {
		lookup = hostenv.Set(env, "PATH", pkg.BinDir)
	}
	entry, err := hostenv.LookPath(s.Command, lookup)
	if err != nil {
		return nil, err
	}
	return cliExecutable(ctx, entry, args, pkg.Dir, env)
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
	dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		return err
	}
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return err
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
	if pkg.BinDir == "" && pkg.CLI["init"] != nil {
		line, err := cliOSCommand(pkg.CLI["init"])
		if err != nil {
			return err
		}
		shellName, args := "bash", []string{"-c", line}
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
		cmd.Env = env
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
	args, err := cliVersionArgs(pkg, s.Command)
	if err != nil {
		return err
	}
	cmd, err := m.cliCommand(ctx, pkg, s, args...)
	if err != nil {
		return err
	}
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err = cmd.Run(); err != nil {
		return &CLIExecutionError{Stage: "versionCheck", ExitCode: exitCode(cmd), Diagnostic: out.String(), Cause: err}
	}
	config, _ := pkg.CLI["versionCheck"].(map[string]any)
	minimum, _ := config["minVersion"].(string)
	output := out.String()
	if pattern, _ := config["versionPattern"].(string); pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return err
		}
		match := re.FindStringSubmatch(output)
		if len(match) < 2 {
			return fmt.Errorf("CLI versionPattern did not match")
		}
		output = match[1]
	}
	if !versionAtLeast(output, minimum) {
		return fmt.Errorf("CLI version is below required %s", minimum)
	}
	return nil
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
		return jsonSubset(actual, expected), nil
	}
	pattern, _ := pkg.CLI["statusMatch"].(string)
	return regexp.MustCompile(pattern).MatchString(output.String()), nil
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
	if ok, err := m.cliStatus(ctx, pkg); err == nil && ok {
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
	cmd, err := m.cliCommand(ctx, pkg, s, step.args...)
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
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CLI login failed or expired; start login again")
	}
	return nil
}

func cliAuthorizationURL(text, domain string) string {
	allowed := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && u.Scheme == "https" && u.User == nil &&
			(u.Hostname() == domain || strings.HasSuffix(u.Hostname(), "."+domain))
	}
	// JSON encoders can escape '&' as \u0026 or '/' as \/. Decode only the
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

func (m *Manager) logoutCLI(ctx context.Context, pkg connector.Package) error {
	s, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	if s.LogoutMode == "delete-config" {
		dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
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
