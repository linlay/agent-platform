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

	"agent-platform/internal/connector"
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
	if err := connector.DecodeJSON(data, &s); err != nil {
		return s, err
	}
	if !regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9._-]+$`).MatchString(s.NPMPackage) || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(s.NPMVersion) {
		return s, fmt.Errorf("CLI requires a pinned npm package and version in platform settings")
	}
	if !connector.ValidID(s.Command) || !regexp.MustCompile(`^[A-Z][A-Z0-9_]*_CONFIG_DIR$`).MatchString(s.ConfigEnv) || strings.HasPrefix(s.ConfigEnv, "AP_") {
		return s, fmt.Errorf("invalid CLI command or isolated config environment")
	}
	if s.Entry == "" || filepath.IsAbs(s.Entry) || strings.ContainsAny(s.Entry, "\\:") || strings.Contains(s.Entry, "..") {
		return s, fmt.Errorf("invalid npm entry path")
	}
	if s.NativeEntry != "" && (filepath.IsAbs(s.NativeEntry) || strings.ContainsAny(s.NativeEntry, "\\:") || strings.Contains(s.NativeEntry, "..")) {
		return s, fmt.Errorf("invalid native entry path")
	}
	if s.LogoutMode != "" && s.LogoutMode != "delete-config" {
		return s, fmt.Errorf("invalid CLI logout mode")
	}
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

func (m *Manager) cliCommand(ctx context.Context, pkg connector.Package, s cliSettings, args ...string) (*exec.Cmd, error) {
	dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return nil, err
	}
	entry := filepath.Join(dir, "npm", "node_modules", filepath.FromSlash(s.NPMPackage), filepath.FromSlash(s.Entry))
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("CLI is not prepared; start connector login to install it")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("Node.js is required for this CLI")
	}
	cmd := exec.CommandContext(ctx, node, append([]string{entry}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), s.ConfigEnv+"="+filepath.Join(dir, "config"))
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

func (m *Manager) prepareCLI(ctx context.Context, pkg connector.Package, s cliSettings) error {
	dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
		return err
	}
	if runtimeConfig, ok := pkg.CLI["runtime"].(map[string]any); ok {
		required, _ := runtimeConfig["version"].(string)
		major, err := strconv.Atoi(strings.TrimPrefix(required, ">="))
		if runtimeConfig["type"] != "node" || err != nil {
			return fmt.Errorf("unsupported CLI runtime requirement")
		}
		node, err := exec.LookPath("node")
		if err != nil {
			return fmt.Errorf("Node.js >=%d is required", major)
		}
		check := exec.CommandContext(ctx, node, "--version")
		data, err := check.Output()
		if err != nil || !versionAtLeast(string(data), strconv.Itoa(major)+".0.0") {
			return fmt.Errorf("Node.js >=%d is required", major)
		}
	}
	// Pin both name and version; initialization never executes shell text from ZIP.
	var installed struct{ Name, Version string }
	packageFile := filepath.Join(dir, "npm", "node_modules", filepath.FromSlash(s.NPMPackage), "package.json")
	if data, err := os.ReadFile(packageFile); err == nil {
		_ = json.Unmarshal(data, &installed)
	}
	if installed.Name != s.NPMPackage || strings.TrimPrefix(installed.Version, "v") != s.NPMVersion {
		npm, err := exec.LookPath("npm")
		if err != nil {
			return fmt.Errorf("npm and Node.js are required to prepare the CLI")
		}
		args := []string{"install", "--prefix", filepath.Join(dir, "npm"), "--cache", filepath.Join(dir, "npm-cache"), "--ignore-scripts", "--no-audit", "--no-fund", s.NPMPackage + "@" + s.NPMVersion}
		if runtime.GOOS == "windows" {
			// npm.cmd is shell text. Run the adjacent npm JS entry through Node.
			entry := filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
			if _, err := os.Stat(entry); err != nil {
				return fmt.Errorf("npm-cli.js is required next to npm.cmd")
			}
			npm, err = exec.LookPath("node")
			if err != nil {
				return fmt.Errorf("Node.js is required")
			}
			args = append([]string{entry}, args...)
		}
		cmd := exec.CommandContext(ctx, npm, args...)
		configureProcess(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("CLI installation failed; check npm, network access and platform support")
		}
	}
	versionArgs, err := cliVersionArgs(pkg, s.Command)
	if err != nil {
		return err
	}
	cmd, err := m.cliCommand(ctx, pkg, s, versionArgs...)
	if err != nil {
		return err
	}
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CLI version check failed")
	}
	versionConfig, _ := pkg.CLI["versionCheck"].(map[string]any)
	minimum, _ := versionConfig["minVersion"].(string)
	versionOutput := output.String()
	if pattern, ok := versionConfig["versionPattern"].(string); ok && pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid CLI versionPattern")
		}
		match := re.FindStringSubmatch(versionOutput)
		if len(match) < 2 {
			return fmt.Errorf("CLI versionPattern did not match")
		}
		versionOutput = match[1]
	}
	if !versionAtLeast(versionOutput, minimum) {
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
	if err := m.prepareCLI(ctx, pkg, s); err != nil {
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
	if s.NativeEntry == "" {
		return nil
	}
	dir, err := StateDir(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	entry := s.NativeEntry
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	info, err := os.Stat(filepath.Join(dir, "npm", "node_modules", filepath.FromSlash(s.NPMPackage), filepath.FromSlash(entry)))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("native CLI is not prepared; start connector login to install it")
	}
	return nil
}
