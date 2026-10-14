package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"
)

type cliSettings struct {
	Command    string `json:"command"`
	ConfigEnv  string `json:"configEnv"`
	LogoutMode string `json:"logoutMode,omitempty"`
}

func cliSettingsFor(pkg connector.Package) (cliSettings, error) {
	var s cliSettings
	data, _ := json.Marshal(pkg.CLI["platform"])
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("invalid CLI platform settings")
	}
	config, ok := pkg.CLI["versionCheck"].(map[string]any)
	if !ok {
		return s, fmt.Errorf("CLI requires versionCheck")
	}
	line, err := cliOSCommand(config["command"])
	if err != nil {
		return s, fmt.Errorf("versionCheck.command: %w", err)
	}
	command, err := cliEntryName(line)
	if err != nil {
		return s, err
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
		if pkg.AuthMode != connector.AuthDelegated {
			return s, fmt.Errorf("only auth_mode=null can declare CLI auth")
		}
		if _, err := cliAuthSteps(pkg); err != nil {
			return s, err
		}
		if _, err := cliOSCommand(pkg.CLI["status"]); err != nil {
			return s, fmt.Errorf("status: %w", err)
		}
		if s.LogoutMode == "" {
			if _, err := cliOSCommand(pkg.CLI["unAuth"]); err != nil {
				return s, fmt.Errorf("unAuth: %w", err)
			}
		}
	}
	if raw, exists := pkg.CLI["statusMatchJson"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return s, fmt.Errorf("statusMatchJson must be a JSON object")
		}
	}
	if raw, exists := pkg.CLI["statusMatch"]; exists {
		pattern, ok := raw.(string)
		if !ok || pattern == "" {
			return s, fmt.Errorf("statusMatch must be a nonempty string")
		}
		if _, err := cliPattern(pattern); err != nil {
			return s, fmt.Errorf("invalid statusMatch")
		}
	}
	minimum, ok := config["minVersion"].(string)
	if !ok || !validCLISemVer(minimum, false) {
		return s, fmt.Errorf("CLI requires SemVer versionCheck.minVersion")
	}
	if raw, exists := config["versionPattern"]; exists {
		pattern, ok := raw.(string)
		if !ok || pattern == "" {
			return s, fmt.Errorf("CLI versionPattern requires a nonempty string")
		}
		re, err := cliPattern(pattern)
		if err != nil || len(re.GetGroupNumbers()) < 2 {
			return s, fmt.Errorf("CLI versionPattern requires a capture group")
		}
	}
	return s, nil
}

// Business execution still resolves one named entry point. Shell syntax in
// lifecycle declarations does not turn a connector grant into an arbitrary
// shell entry point. The first non-builtin command identifies that entry.
func cliEntryName(line string) (string, error) {
	if runtime.GOOS == "windows" {
		line = strings.ReplaceAll(line, `\`, `/`)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return "", fmt.Errorf("versionCheck command requires a named executable")
	}
	entry := ""
	syntax.Walk(file, func(n syntax.Node) bool {
		if entry != "" {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		word := cliLiteralWord(call.Args[0])
		if word == "" {
			return true
		}
		switch word {
		case "set", "test", "[", "true", ":", "export", "echo", "printf":
			return true
		case "exec", "command":
			if len(call.Args) > 1 {
				word = cliLiteralWord(call.Args[1])
			}
		}
		name := strings.TrimSuffix(filepath.Base(word), ".cmd")
		if connector.ValidID(name) {
			entry = name
		}
		return true
	})
	if entry == "" {
		return "", fmt.Errorf("versionCheck command requires a named executable")
	}
	return entry, nil
}

func cliLiteralWord(word *syntax.Word) string {
	literal := true
	syntax.Walk(word, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp, *syntax.ExtGlob:
			literal = false
		}
		return literal
	})
	if !literal {
		return ""
	}
	var text strings.Builder
	if err := syntax.NewPrinter().Print(&text, word); err != nil {
		return ""
	}
	values, err := shell.Fields(text.String(), func(string) string { return "" })
	if err != nil || len(values) != 1 {
		return ""
	}
	return values[0]
}

type cliAuthStep struct {
	command, skipCommand string
	domain               string
	waitForExit          bool
}

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
		step := cliAuthStep{waitForExit: true}
		for _, source := range []map[string]any{pkg.CLI, fields} {
			if raw, exists := source["authUrlDomain"]; exists {
				var ok bool
				step.domain, ok = raw.(string)
				if !ok || !validCLIAuthDomain(step.domain) {
					return nil, fmt.Errorf("CLI authUrlDomain requires an exact hostname")
				}
			}
			if raw, exists := source["authWaitForExit"]; exists {
				var ok bool
				step.waitForExit, ok = raw.(bool)
				if !ok {
					return nil, fmt.Errorf("CLI authWaitForExit must be boolean")
				}
			}
			if raw, exists := source["authSuppressBrowser"]; exists {
				if _, ok := raw.(bool); !ok {
					return nil, fmt.Errorf("CLI authSuppressBrowser must be boolean")
				}
			}
		}
		var err error
		step.command, err = cliOSCommand(fields["command"])
		if err != nil {
			return nil, fmt.Errorf("auth command: %w", err)
		}
		if raw, exists := fields["skipIf"]; exists {
			step.skipCommand, err = cliOSCommand(raw)
			if err != nil {
				return nil, fmt.Errorf("skipIf: %w", err)
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func validCLIAuthDomain(domain string) bool {
	return domain != "" && regexp.MustCompile(`(?i)^[a-z0-9]+(?:[.-][a-z0-9]+)*$`).MatchString(domain)
}

// cliArgs retains the literal-only adapter for callers constructing direct argv;
// lifecycle commands use cliOSCommand and the declared operating-system shell.
func cliArgs(pkg connector.Package, key, command string) ([]string, error) {
	line, err := cliOSCommand(pkg.CLI[key])
	if err != nil {
		return nil, err
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, fmt.Errorf("CLI %s must be one command", key)
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(stmt.Redirs) > 0 || stmt.Background || stmt.Negated {
		return nil, fmt.Errorf("CLI direct command cannot contain shell operations")
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
		return nil, fmt.Errorf("CLI direct command cannot contain expansion")
	}
	args, err := shell.Fields(line, func(string) string { return "" })
	if err != nil || len(args) == 0 || strings.TrimSuffix(args[0], ".cmd") != command {
		return nil, fmt.Errorf("CLI %s must invoke its managed executable", key)
	}
	return args[1:], nil
}

func cliOSCommand(raw any) (string, error) {
	return cliOSCommandFor(raw, runtime.GOOS)
}

func cliOSCommandFor(raw any, goos string) (string, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("OS command map is required")
	}
	var commands map[string]string
	if json.Unmarshal(data, &commands) != nil || len(commands) == 0 {
		return "", fmt.Errorf("OS command map is required")
	}
	for key, value := range commands {
		if key != "darwin" && key != "linux" && key != "win32" || strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("invalid OS command map")
		}
	}
	key := goos
	if key == "windows" {
		key = "win32"
	}
	line, ok := commands[key]
	if !ok {
		return "", fmt.Errorf("UNSUPPORTED_CAPABILITY: command for %s is required", key)
	}
	return line, nil
}

func (m *Manager) cliEnvironment(pkg connector.Package) ([]string, error) {
	env, err := CLIBaseEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	return CLIInstallationEnvironment(pkg, env)
}

func (m *Manager) cliPhaseEnvironment(ctx context.Context, pkg connector.Package, phase CLIPhase) ([]string, error) {
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	binding, err := CLIEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	values, err := ResolveCLIEnvironment(ctx, binding, m.identityFile, phase)
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	// Token validation uses a temporary state root and must not see an earlier
	// account's login files. Other lifecycle calls share the connector's home.
	state, err := pkg.ConnectorStateDir()
	if err != nil {
		return nil, err
	}
	env, err = applyCLIPrivateEnvironment(pkg, env)
	if err != nil {
		return nil, err
	}
	bin := hostenv.Value(env, "CONNECTOR_BIN_DIR")
	install := filepath.Dir(bin)
	programState := state
	if pkg.InstallationRoot != "" {
		programState, err = StateDir(pkg.InstallationRoot, pkg.ID)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(programState, 0700); err != nil {
			return nil, err
		}
	}
	root, err := filepath.Rel(programState, bin)
	if err != nil {
		return nil, err
	}
	if err := preparePrivateDirectories(programState, root); err != nil {
		return nil, err
	}
	if phase == CLIPhaseInit {
		prefix := install
		if runtime.GOOS == "windows" {
			prefix = bin
		}
		// npm's global install flag is scoped to this package version, never the
		// user's system/global npm prefix. Package commands remain unchanged.
		env = hostenv.Set(env, "npm_config_prefix", prefix)
		env = hostenv.Set(env, "NPM_CONFIG_PREFIX", prefix)
		env = hostenv.Set(env, "npm_config_cache", filepath.Join(install, "npm-cache"))
	}
	return env, nil
}

func (m *Manager) cliLifecycleCommand(ctx context.Context, pkg connector.Package, phase CLIPhase, line string) (*exec.Cmd, error) {
	env, err := m.cliPhaseEnvironment(ctx, pkg, phase)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(line, "\r\n") {
		// cmd.exe /c does not consistently execute an embedded multiline batch.
		// Keep its exact text in this connector's private temporary directory.
		script, err := os.CreateTemp(hostenv.Value(env, "TEMP"), ".lifecycle-*.cmd")
		if err != nil {
			return nil, err
		}
		path := script.Name()
		if _, err := script.WriteString(line); err != nil {
			script.Close()
			os.Remove(path)
			return nil, err
		}
		if err := script.Close(); err != nil {
			os.Remove(path)
			return nil, err
		}
		context.AfterFunc(ctx, func() { _ = os.Remove(path) })
		line = "\"\"" + path + "\"\""
	}
	return cliShellCommand(ctx, pkg.Dir, env, line, runtime.GOOS)
}

func cliShellCommand(ctx context.Context, dir string, env []string, line, goos string) (*exec.Cmd, error) {
	entry, args := "/bin/sh", []string{"-c", line}
	if goos == "windows" {
		var err error
		entry, err = hostenv.LookPath("cmd.exe", env)
		if err != nil {
			return nil, err
		}
		args = []string{"/d", "/s", "/c", line}
	}
	cmd := exec.CommandContext(ctx, entry, args...)
	cmd.Dir, cmd.Env = dir, env
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

var (
	errCLIUnavailable   = errors.New("CLI command is not installed")
	errCLIVersionTooLow = errors.New("CLI version is below minimum")
)

// prepareCLI checks first, installs only an unavailable/old version, and checks
// again before publishing readiness. Package bin directories are ordinary PATH
// entries; their presence never suppresses a necessary explicit init.
func (m *Manager) prepareCLI(ctx context.Context, pkg connector.Package, s cliSettings) error {
	env, err := m.cliPhaseEnvironment(ctx, pkg, CLIPhaseVersionCheck)
	if err != nil {
		return err
	}
	if requirement, ok := pkg.CLI["runtime"].(map[string]any); ok {
		if err := m.checkCLIRuntime(ctx, pkg, env, requirement); err != nil {
			return err
		}
	}
	err = m.checkCLIVersion(ctx, pkg)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errCLIUnavailable) && !errors.Is(err, errCLIVersionTooLow) {
		return err
	}
	if pkg.CLI["init"] == nil {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: installed CLI does not satisfy versionCheck and no init is declared")
	}
	line, err := cliOSCommand(pkg.CLI["init"])
	if err != nil {
		return err
	}
	if err := validateNPMInit(line); err != nil {
		return err
	}
	initCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd, err := m.cliLifecycleCommand(initCtx, pkg, CLIPhaseInit, line)
	if err != nil {
		return err
	}
	// Only a bounded, redacted excerpt reaches the preparation diagnostic;
	// the error returned to ordinary callers remains a fixed summary.
	var out boundedOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return &CLIExecutionError{Stage: "init", ExitCode: exitCode(cmd), Diagnostic: cliPreparationDiagnostic("CLI initialization failed", out.String(), pkg, cmd.Env), Cause: err}
	}
	hostenv.Refresh()
	if err := m.checkCLIVersion(ctx, pkg); err != nil {
		return err
	}
	return nil
}

func (m *Manager) checkCLIRuntime(ctx context.Context, pkg connector.Package, env []string, requirement map[string]any) error {
	typeName, _ := requirement["type"].(string)
	required, _ := requirement["version"].(string)
	if typeName != "node" {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: unsupported CLI runtime")
	}
	minimum := strings.TrimPrefix(required, ">=")
	if minimum == required && strings.ContainsAny(required, "<>=^~*| ") {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: unsupported CLI runtime version constraint")
	}
	if regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))?$`).MatchString(minimum) {
		for strings.Count(minimum, ".") < 2 {
			minimum += ".0"
		}
	}
	if !validCLISemVer(minimum, false) {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: unsupported CLI runtime version constraint")
	}
	node, err := hostenv.LookPath("node", env)
	if err != nil {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: Node.js %s is required", required)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := cliExecutable(checkCtx, node, []string{"--version"}, pkg.Dir, env)
	if err != nil {
		return err
	}
	var out boundedOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err = cmd.Run(); err != nil || !versionAtLeast(out.String(), minimum) {
		return fmt.Errorf("UNSUPPORTED_CAPABILITY: Node.js %s is required", required)
	}
	return nil
}

func (m *Manager) checkCLIVersion(ctx context.Context, pkg connector.Package) error {
	config, _ := pkg.CLI["versionCheck"].(map[string]any)
	line, err := cliOSCommand(config["command"])
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := m.cliLifecycleCommand(checkCtx, pkg, CLIPhaseVersionCheck, line)
	if err != nil {
		return err
	}
	var out, diagnostic boundedOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		cause := err
		if checkCtx.Err() == nil && (exitCode(cmd) == 127 || exitCode(cmd) == 9009) {
			cause = errCLIUnavailable
		}
		return &CLIExecutionError{Stage: "versionCheck", ExitCode: exitCode(cmd), Diagnostic: cliPreparationDiagnostic("CLI version check failed", diagnostic.String()+"\n"+out.String(), pkg, cmd.Env), Cause: cause}
	}
	output := out.String()
	if pattern, _ := config["versionPattern"].(string); pattern != "" {
		re, err := cliPattern(pattern)
		if err != nil {
			return err
		}
		match, err := re.FindStringMatch(output)
		if err != nil {
			return cliVersionValidationError("CLI versionPattern matching timed out", err)
		}
		if match == nil || match.GroupByNumber(1) == nil || len(match.GroupByNumber(1).Captures) == 0 {
			return cliVersionValidationError("CLI version output does not match versionPattern; check the pattern and JSON escaping", nil)
		}
		output = match.GroupByNumber(1).String()
	}
	if !validCLISemVer(strings.TrimSpace(output), true) {
		return cliVersionValidationError("CLI version output must be SemVer or declare versionPattern", nil)
	}
	minimum, _ := config["minVersion"].(string)
	if !versionAtLeast(output, minimum) {
		return cliVersionValidationError("CLI version is below required minimum", errCLIVersionTooLow)
	}
	return nil
}

// npm commands retain the package's shell text, but install arguments must pin
// every package. This prevents an explicit prepare from resolving latest or a
// semver range into a different shared executable on different machines.
func validateNPMInit(line string) error {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		// Windows batch has a different grammar; inspect literal command words
		// rather than attempting to rewrite it using POSIX shell semantics.
		if runtime.GOOS == "windows" {
			return validateNPMWords(strings.Fields(line))
		}
		return fmt.Errorf("invalid CLI init shell command")
	}
	var validation error
	syntax.Walk(file, func(n syntax.Node) bool {
		if validation != nil {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		words := make([]string, len(call.Args))
		for i, word := range call.Args {
			words[i] = cliLiteralWord(word)
		}
		if len(words) > 0 && strings.TrimSuffix(filepath.Base(words[0]), ".cmd") == "npm" {
			for _, assignment := range call.Assigns {
				if strings.EqualFold(assignment.Name.Value, "npm_config_prefix") || strings.EqualFold(assignment.Name.Value, "npm_config_userconfig") {
					validation = fmt.Errorf("CLI npm init must use the platform's private prefix")
					return false
				}
			}
		}
		validation = validateNPMWords(words)
		return validation == nil
	})
	return validation
}

func validateNPMWords(words []string) error {
	if len(words) < 2 || strings.TrimSuffix(filepath.Base(words[0]), ".cmd") != "npm" {
		return nil
	}
	start, global := -1, false
	for i, word := range words[1:] {
		if strings.HasPrefix(word, "--prefix") || strings.HasPrefix(word, "--userconfig") {
			return fmt.Errorf("CLI npm init must use the platform's private prefix")
		}
		if word == "install" || word == "i" {
			start = i + 2
		}
		if word == "--global" || word == "-g" {
			global = true
		}
	}
	if start < 0 {
		return nil
	}
	if !global {
		return fmt.Errorf("CLI npm init must use global installation in the private prefix")
	}
	packages := 0
	for i := start; i < len(words); i++ {
		word := strings.Trim(words[i], `"'`)
		if strings.HasPrefix(word, "-") {
			// Common npm options with a separate public value are not packages.
			if word == "--loglevel" || word == "--registry" || word == "--cache" {
				i++
			}
			continue
		}
		at := strings.LastIndexByte(word, '@')
		if at <= 0 || !validCLISemVer(word[at+1:], false) {
			return fmt.Errorf("CLI npm init must pin an exact package version")
		}
		packages++
	}
	if packages == 0 {
		return fmt.Errorf("CLI npm init must pin an exact package version")
	}
	return nil
}

var cliSemVerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)

func validCLISemVer(value string, allowV bool) bool {
	if allowV {
		value = strings.TrimPrefix(value, "v")
	}
	parts := cliSemVerPattern.FindStringSubmatch(value)
	if parts == nil {
		return false
	}
	for i, section := range []string{parts[4], parts[5]} {
		if section == "" {
			continue
		}
		for _, part := range strings.Split(section, ".") {
			if part == "" || i == 0 && strings.Trim(part, "0123456789") == "" && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}

func compareNumericIdentifier(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func versionAtLeast(output, minimum string) bool {
	output = strings.TrimSpace(output)
	if !validCLISemVer(output, true) || !validCLISemVer(minimum, false) {
		return false
	}
	a := cliSemVerPattern.FindStringSubmatch(strings.TrimPrefix(output, "v"))
	b := cliSemVerPattern.FindStringSubmatch(minimum)
	for i := 1; i <= 3; i++ {
		if compared := compareNumericIdentifier(a[i], b[i]); compared != 0 {
			return compared > 0
		}
	}
	if a[4] == "" || b[4] == "" {
		return a[4] == "" // A release outranks its prereleases; metadata is ignored.
	}
	ap, bp := strings.Split(a[4], "."), strings.Split(b[4], ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		an, bn := strings.Trim(ap[i], "0123456789") == "", strings.Trim(bp[i], "0123456789") == ""
		compared := 0
		if an && bn {
			compared = compareNumericIdentifier(ap[i], bp[i])
		} else if an != bn {
			if an {
				compared = -1
			} else {
				compared = 1
			}
		} else {
			compared = strings.Compare(ap[i], bp[i])
		}
		if compared != 0 {
			return compared > 0
		}
	}
	return len(ap) >= len(bp)
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

// Exit 1 is a common local "not logged in yet" convention. It is inconclusive
// for credential verification, but must not prevent an explicitly requested
// login or cancel that login while its status is still pending.
var errCLIStatusUnconfirmed = errors.New("CLI authorization status is not yet confirmed")

func cliStatusAllowsLogin(err error) bool {
	return err == nil || errors.Is(err, ErrTokenRejected) || errors.Is(err, errCLIStatusUnconfirmed)
}

func (m *Manager) cliStatus(ctx context.Context, pkg connector.Package) (bool, error) {
	if _, err := cliSettingsFor(pkg); err != nil {
		return false, err
	}
	// An observational status check never starts an unprepared wrapper, which
	// could have an implicit native binary download as a side effect.
	if err := m.requireNativeCLI(pkg); err != nil {
		return false, err
	}
	line, err := cliOSCommand(pkg.CLI["status"])
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := m.cliLifecycleCommand(ctx, pkg, CLIPhaseStatus, line)
	if err != nil {
		return false, err
	}
	var output, diagnostic boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	err = cmd.Run()
	if ctx.Err() != nil {
		return false, fmt.Errorf("CLI authorization status timed out")
	}
	if err != nil {
		structuredFailure := false
		for _, stream := range []string{diagnostic.String(), output.String()} {
			if authErr := cliStatusAuthenticationError(stream); authErr != nil {
				return false, authErr
			}
			var result struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(stream), &result) == nil && len(result.Error) > 0 && string(result.Error) != "null" {
				structuredFailure = true
			}
		}
		var exit *exec.ExitError
		if !structuredFailure && errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, errCLIStatusUnconfirmed
		}
		// A timeout, startup problem, nonzero exit, or arbitrary stderr is not
		// evidence that a previously valid account has become unauthorized.
		return false, fmt.Errorf("CLI authorization status command failed")
	}
	if expected, ok := pkg.CLI["statusMatchJson"].(map[string]any); ok {
		var actual map[string]any
		if err := json.Unmarshal([]byte(output.String()), &actual); err != nil || actual == nil {
			return false, fmt.Errorf("CLI authorization status was not a JSON object")
		}
		if _, legacy := pkg.CLI["platform"]; legacy {
			// Published launcher extensions historically returned ok/data
			// envelopes. Keep that compatibility only for those extensions;
			// standard declarations match the entire JSON object verbatim.
			if actual["ok"] == false {
				return false, nil
			}
			if actual["ok"] == true {
				if data, ok := actual["data"].(map[string]any); ok {
					actual = data
				}
			}
		}
		if !jsonSubset(actual, expected) {
			return false, nil
		}
	}
	if pattern, _ := pkg.CLI["statusMatch"].(string); pattern != "" {
		re, err := cliPattern(pattern)
		if err != nil {
			return false, err
		}
		matched, err := re.MatchString(output.String())
		if err != nil {
			return false, fmt.Errorf("CLI status pattern matching timed out")
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

// Only a complete machine-readable error object can reject credentials. Neither
// arbitrary diagnostics nor a generic 403 proves the account is unauthenticated.
func cliStatusAuthenticationError(output string) error {
	var result struct {
		Error *struct {
			Code       json.RawMessage `json:"code"`
			HTTPStatus json.RawMessage `json:"http_status"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(output), &result) != nil || result.Error == nil {
		return nil
	}
	var code string
	_ = json.Unmarshal(result.Error.Code, &code)
	switch code {
	case "AUTHENTICATION_DENIED":
		return ErrCredentialAuthenticationDenied
	case "AUTH_FAILED", "AUTH_REQUIRED", "UNAUTHORIZED", "INVALID_TOKEN", "CREDENTIALS_MISSING":
		return ErrTokenRejected
	}
	var status int
	if json.Unmarshal(result.Error.HTTPStatus, &status) == nil && status == 401 {
		return ErrTokenRejected
	}
	return nil
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
	_, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	if err := m.requirePrepared(pkg); err != nil {
		return err
	}
	if ok, err := m.cliStatus(ctx, pkg); !cliStatusAllowsLogin(err) {
		return err
	} else if ok {
		return nil
	}
	steps, err := cliAuthSteps(pkg)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.skipCommand != "" {
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			check, err := m.cliLifecycleCommand(checkCtx, pkg, CLIPhaseSkipIf, step.skipCommand)
			if err != nil {
				cancel()
				return err
			}
			// Configuration output may contain login materials; discard it.
			err = check.Run()
			checkExpired := checkCtx.Err()
			cancel()
			if checkExpired != nil {
				return fmt.Errorf("CLI authorization prerequisite check timed out")
			}
			if err == nil {
				continue
			}
			if exitCode(check) != 1 {
				return fmt.Errorf("CLI authorization prerequisite check failed")
			}
		}
		m.mu.Lock()
		l.URL, l.Status, l.Message = "", "preparing", "Preparing authorization step"
		m.mu.Unlock()
		if err := m.runCLIAuthStep(ctx, pkg, l, step); err != nil {
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

func (m *Manager) runCLIAuthStep(ctx context.Context, pkg connector.Package, l *login, step cliAuthStep) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd, err := m.cliLifecycleCommand(ctx, pkg, CLIPhaseAuth, step.command)
	if err != nil {
		return err
	}
	output := boundedOutput{onWrite: func(text string) {
		if raw := cliAuthorizationURL(text, step.domain); raw != "" {
			m.setURL(l, raw)
		}
	}}
	cmd.Stdout, cmd.Stderr = &output, &output
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
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("CLI login failed or expired; start login again")
			}
			return nil
		case <-ctx.Done():
			<-done // The context terminates and reaps the dedicated process tree.
			return fmt.Errorf("CLI login canceled or expired")
		case <-ticker.C:
			ok, err := m.cliStatus(ctx, pkg)
			if !cliStatusAllowsLogin(err) || ok {
				cancel()
				<-done
				if err != nil {
					return err
				}
				return nil
			}
		}
	}
}

func cliAuthorizationURL(text, domain string) string {
	allowed := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			return false
		}
		host := u.Hostname()
		if u.Scheme == "http" {
			ip := net.ParseIP(host)
			return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
		}
		return u.Scheme == "https" && domain != "" && strings.EqualFold(host, domain)
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
		return fmt.Errorf("CLI sign-out requires an explicit unAuth command; reset configuration separately")
	}
	if err := m.requireNativeCLI(pkg); err != nil {
		return err
	}
	line, err := cliOSCommand(pkg.CLI["unAuth"])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := m.cliLifecycleCommand(ctx, pkg, CLIPhaseUnAuth, line)
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CLI logout failed")
	}
	return nil
}

func (m *Manager) requireNativeCLI(pkg connector.Package) error {
	return m.requirePrepared(pkg)
}
