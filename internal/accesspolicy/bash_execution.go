package accesspolicy

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/builtins"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// BashExecution is analysis only: the original command is never rewritten.
type BashExecution struct {
	Connector          bool
	Argv               []string
	Cwd                string
	Program            string
	Script             string
	Identity           string
	Opaque             bool
	Uncertain          bool
	Wrapped            bool
	TrustedInterpreter bool
	BlockReason        string
}

var ordinaryCommands = strings.Fields("ls cat head tail top free df git rg dbx httpx pdftotext find echo printf sed awk grep wc sort uniq tr cut cd stat file du test which mkdir touch cp mv rm ln chmod date curl wget pwd true false sleep uname whoami id basename dirname readlink realpath tee printenv Get-ChildItem Get-Content Remove-Item")
var shellBuiltins = wordSet("echo printf pwd cd test true false type read export unset declare local typeset set shift break continue return :")
var wrapperNames = wordSet("env command builtin nohup nice timeout stdbuf")
var systemExecutables = captureSystemExecutables()

func wordSet(words string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(words) {
		out[strings.ToLower(word)] = true
	}
	return out
}
func commandFamily(name string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(name, "\\", "/"))), ".exe")
}
func captureSystemExecutables() map[string]string {
	out := map[string]string{}
	names := append(append([]string{}, ordinaryCommands...), strings.Fields("env nohup nice timeout stdbuf bash sh zsh dash ksh python python3 node ruby perl powershell pwsh cmd go npm npx yarn pnpm pip make")...)
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			if c, err := pathutil.Canonicalize(p); err == nil {
				out[commandFamily(name)] = c.Key
			}
		}
	}
	return out
}

func resolvedProgram(name, cwd string, variables map[string]string) string {
	if strings.ContainsAny(name, "/\\") || filepath.IsAbs(name) {
		return resolveAgainstCwd(name, cwd)
	}
	path := os.Getenv("PATH")
	if value, ok := variables["PATH"]; ok {
		path = value
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = cwd
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		suffixes := []string{""}
		if runtime.GOOS == "windows" {
			suffixes = append(suffixes, ".exe", ".com", ".bat", ".cmd")
		}
		for _, suffix := range suffixes {
			p := filepath.Join(dir, name+suffix)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && (info.Mode()&0o111 != 0 || runtime.GOOS == "windows") {
				return p
			}
		}
	}
	return ""
}

func recognizedProgram(session QuerySession, name, cwd string, vars map[string]string, env *BashEnvironment, allowBuiltins bool) bool {
	base := commandFamily(name)
	bare := !strings.ContainsAny(name, "/\\")
	if allowBuiltins && bare && (shellBuiltins[base] || base == "command" || base == "builtin") {
		return true
	}
	if session.AgentHasRuntimeSandbox {
		if env == nil || env.Resolve == nil {
			return false
		}
		p, err := env.Resolve(name, cwd, vars)
		if err != nil {
			return false
		}
		// Resolve the same command's system installation in the container. Merely
		// landing under /usr/bin is insufficient: ./ls could point to python.
		// Canonical comparison still permits sh -> dash and versioned Python.
		for _, root := range []string{"/usr/bin/", "/bin/", "/usr/local/bin/"} {
			if system, err := env.Resolve(root+base, cwd, vars); err == nil && system == p {
				return true
			}
		}
		return false
	}
	p := resolvedProgram(name, cwd, vars)
	if p == "" {
		return false
	}
	canonical, err := pathutil.Canonicalize(p)
	if err != nil {
		return false
	}
	if systemExecutables[base] == canonical.Key {
		return true
	}
	if base == "rg" || base == "dbx" || base == "httpx" || base == "pdftotext" {
		if builtin, err := builtins.ResolveProcessBuiltin(base); err == nil {
			if c, err := pathutil.Canonicalize(builtin); err == nil && c.Key == canonical.Key {
				return true
			}
		}
	}
	return false
}

func isOrdinaryCommand(base string) bool {
	if shellBuiltins[base] {
		return true
	}
	for _, s := range ordinaryCommands {
		if strings.EqualFold(s, base) {
			return true
		}
	}
	return false
}

func analyzeBashExecution(session QuerySession, cmd bashast.SimpleCommand, cwd string, variables map[string]string, env *BashEnvironment) (x BashExecution) {
	x = BashExecution{Argv: cmd.Argv, Cwd: cwd, Uncertain: cmd.Uncertain}
	allowBuiltins := true
	vars := CloneStringMap(variables)
	if vars == nil {
		vars = map[string]string{}
	}
	for key, value := range cmd.Variables {
		vars[key] = value
	}
	for _, assignment := range cmd.EnvVars {
		vars[assignment.Name] = assignment.Value
	}
	defer func() { x.Connector = connectorExecution(session, x, vars, env) }()
	for depth := 0; depth < 8 && len(x.Argv) > 0; depth++ {
		name := x.Argv[0]
		base := commandFamily(name)
		if containsUnresolvedPlaceholder(name) {
			x.Uncertain = true
			break
		}
		known := recognizedProgram(session, name, x.Cwd, vars, env, allowBuiltins)
		if wrapperNames[base] && known {
			x.Wrapped = true
			if base != "command" && base != "builtin" {
				// exec-style wrappers use PATH, not the invoking shell's builtins.
				allowBuiltins = false
			}
			next, nextCwd, ok := unwrapExecution(base, x.Argv[1:], x.Cwd, vars)
			if !ok {
				x.Uncertain = true
				break
			}
			x.Argv, x.Cwd = next, nextCwd
			if !session.AgentHasRuntimeSandbox {
				if normalized, err := NormalizePath(x.Cwd); err == nil {
					x.Cwd = normalized
				} else {
					x.Uncertain = true
					return x
				}
			} else if env != nil && env.Directory != nil {
				if canonical, err := env.Directory(x.Cwd); err == nil {
					x.Cwd = canonical
				} else {
					x.Uncertain = true
					if errors.Is(err, ErrBashTemporaryEscape) {
						x.BlockReason = err.Error()
					}
				}
			}
			if len(next) == 0 {
				return x
			}
			if depth == 7 {
				x.Uncertain = true
			}
			continue
		}
		x.Identity = base
		if isOpaqueCommand(base) && known {
			x.Opaque = true
			x.TrustedInterpreter = isInterpreter(base)
			x.Script = interpreterScript(base, x.Argv[1:])
			if x.Script != "" && session.AgentHasRuntimeSandbox && env != nil && env.Canonical != nil {
				resolved, err := env.Canonical(x.Script, x.Cwd)
				if err != nil {
					x.Uncertain = true
					if errors.Is(err, ErrBashTemporaryEscape) {
						x.BlockReason = err.Error()
					}
				} else {
					x.Script = resolved
				}
			}
			return x
		}
		if known && isOrdinaryCommand(base) {
			return x
		}
		x.Opaque = true
		if session.AgentHasRuntimeSandbox {
			if env != nil && env.Resolve != nil {
				var err error
				x.Program, err = env.Resolve(name, x.Cwd, vars)
				if errors.Is(err, ErrBashTemporaryEscape) {
					x.BlockReason = err.Error()
				}
			}
			if x.Program == "" && strings.ContainsAny(name, "/\\") {
				x.Program = resolveAgainstCwd(name, x.Cwd)
			}
		} else {
			x.Program = resolvedProgram(name, x.Cwd, vars)
		}
		if x.Program == "" && strings.ContainsAny(name, "/\\") {
			x.Program = resolveAgainstCwd(name, x.Cwd)
		}
		if x.Program != "" {
			x.Identity = x.Program
			if session.AgentHasRuntimeSandbox && env != nil && env.Inspect != nil {
				if header, _, err := env.Inspect(x.Program); err == nil && !strings.ContainsRune(header, 0) {
					x.Script = x.Program
					if family := shebangFamily(header, session, x.Cwd, vars, env); family != "" {
						x.Identity = family
					}
				}
				return x
			}
			if host, ok := executableHostPath(session, x.Program); ok {
				if canonical, err := pathutil.Canonicalize(host); err == nil {
					x.Identity = canonical.Key
				}
				if script, header := scriptHeader(host); script {
					x.Script = x.Program
					family := shebangFamily(header, session, x.Cwd, vars, env)
					if family != "" {
						x.Identity = family
					}
				}
			}
		}
		return x
	}
	return x
}

// Only mapped container content may be inspected for run-authored provenance.
// /tmp keeps its historical path exception but is not a host-content mount.
func executableHostPath(session QuerySession, path string) (string, bool) {
	if !session.AgentHasRuntimeSandbox {
		p, err := ResolveSessionPath(session, path)
		return p, err == nil
	}
	for _, roots := range []struct{ guest, host string }{
		{session.RuntimeContext.SandboxPaths.WorkspaceDir, SessionWorkspaceRoot(session)},
		{session.RuntimeContext.SandboxPaths.ChatDir, SessionChatDir(session)},
	} {
		if roots.guest == "" || roots.host == "" {
			continue
		}
		guest := strings.TrimRight(filepath.ToSlash(roots.guest), "/")
		if path == guest || strings.HasPrefix(filepath.ToSlash(path), guest+"/") {
			p, err := ResolveSessionPath(session, path)
			return p, err == nil
		}
		// Relative command operands have already been resolved to the frozen host cwd.
		p, err := pathutil.Canonicalize(path)
		r, rootErr := pathutil.Canonicalize(roots.host)
		if err == nil && rootErr == nil && pathutil.WithinRoot(p, r) {
			return p.Host, true
		}
	}
	return "", false
}

func scriptHeader(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil || strings.ContainsRune(string(data), 0) {
		return false, ""
	}
	return true, string(data)
}

func shebangFamily(header string, session QuerySession, cwd string, vars map[string]string, environment *BashEnvironment) string {
	line := strings.SplitN(header, "\n", 2)[0]
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	words := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(words) == 0 {
		return ""
	}
	base := commandFamily(words[0])
	if base == "env" {
		if !recognizedProgram(session, words[0], cwd, vars, environment, false) {
			return ""
		}
		// env -S quoting is deliberately not guessed from a shebang string.
		localVars := CloneStringMap(vars)
		if localVars == nil {
			localVars = map[string]string{}
		}
		args, dir, ok := unwrapExecution("env", words[1:], cwd, localVars)
		if !ok || len(args) == 0 {
			return ""
		}
		words, cwd, vars = args, dir, localVars
		base = commandFamily(words[0])
	}
	if isInterpreter(base) && recognizedProgram(session, words[0], cwd, vars, environment, false) {
		return base
	}
	return ""
}

func isInterpreter(base string) bool {
	switch base {
	case "bash", "sh", "zsh", "dash", "ksh", "python", "python3", "node", "ruby", "perl", "powershell", "pwsh", "cmd":
		return true
	}
	return false
}

func interpreterScript(base string, args []string) string {
	if !isInterpreter(base) {
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		lower := strings.ToLower(a)
		if base == "powershell" || base == "pwsh" {
			switch lower {
			case "-file", "-f":
				if i+1 < len(args) {
					return args[i+1]
				}
				return ""
			case "-noprofile", "-noninteractive", "-nologo":
				continue
			case "-executionpolicy":
				i++
				continue
			default:
				return ""
			}
		}
		if base == "cmd" {
			if lower == "/c" && i+1 < len(args) {
				p := args[i+1]
				ext := strings.ToLower(filepath.Ext(p))
				if ext == ".bat" || ext == ".cmd" {
					return p
				}
			}
			return ""
		}
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
		if a == "-" {
			return ""
		}
		switch base {
		case "bash", "sh", "zsh", "dash", "ksh":
			if a == "--noprofile" || a == "--norc" {
				continue
			}
			if a == "-o" || a == "-O" {
				i++
				continue
			}
			if strings.Trim(a[1:], "euxvnrfBEmPT") != "" {
				return ""
			}
		case "python", "python3":
			if a == "-W" || a == "-X" {
				i++
				continue
			}
			if strings.Trim(a[1:], "uBIOEsSqv") != "" {
				return ""
			}
		case "node":
			return ""
		case "ruby":
			if a != "-w" {
				return ""
			}
		case "perl":
			if a != "-w" {
				return ""
			}
		default:
			return ""
		}
	}
	return ""
}

func unwrapExecution(base string, args []string, cwd string, vars map[string]string) ([]string, string, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		switch base {
		case "command", "builtin":
			if a == "-v" || a == "-V" {
				return nil, cwd, true
			}
			if a == "-p" {
				return nil, cwd, false
			}
		case "env":
			if name, value, ok := strings.Cut(a, "="); ok && !strings.HasPrefix(name, "-") {
				vars[name] = value
				i++
				continue
			}
			if a == "-i" || a == "--ignore-environment" {
				vars["PATH"] = ""
				i++
				continue
			}
			if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" {
				if i+1 >= len(args) {
					return nil, cwd, false
				}
				if a == "-C" || a == "--chdir" {
					cwd = resolveAgainstCwd(args[i+1], cwd)
				} else {
					vars[args[i+1]] = ""
				}
				i += 2
				continue
			}
			if strings.HasPrefix(a, "--chdir=") {
				cwd = resolveAgainstCwd(strings.TrimPrefix(a, "--chdir="), cwd)
				i++
				continue
			}
		case "nice":
			if a == "-n" || a == "--adjustment" {
				if i+1 >= len(args) {
					return nil, cwd, false
				}
				i += 2
				continue
			}
			if _, err := strconv.Atoi(a); strings.HasPrefix(a, "-") && err == nil {
				i++
				continue
			}
		case "timeout":
			if a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" {
				if i+1 >= len(args) {
					return nil, cwd, false
				}
				i += 2
				continue
			}
			if a == "--foreground" || a == "--preserve-status" {
				i++
				continue
			}
			if !strings.HasPrefix(a, "-") {
				i++
				if i < len(args) {
					return args[i:], cwd, true
				}
				return nil, cwd, false
			}
		case "stdbuf":
			if a == "-i" || a == "-o" || a == "-e" {
				if i+1 >= len(args) {
					return nil, cwd, false
				}
				i += 2
				continue
			}
			if len(a) > 2 && (strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "-e")) {
				i++
				continue
			}
		}
		if strings.HasPrefix(a, "-") {
			return nil, cwd, false
		}
		break
	}
	if i >= len(args) {
		return nil, cwd, false
	}
	return args[i:], cwd, true
}
