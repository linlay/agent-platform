package hostshell

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/shellreview/dialect"
)

func TestToolLaunchIdentityTemplatesAndFingerprints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix Bash launch contract")
	}
	if _, err := systemToolShell("bash", toolSystemShells); err != nil {
		t.Skip("Bash unavailable")
	}
	opts := Options{GOOS: runtime.GOOS, Command: "echo ok", CWD: t.TempDir(), Env: os.Environ()}
	launch, err := ResolveTool(config.BashConfig{}, opts)
	if err != nil || !launch.Binding.SyntaxSupported || launch.Binding.Dialect != dialect.Bash {
		t.Fatalf("default launch: %+v %v", launch.Binding, err)
	}
	if !filepath.IsAbs(launch.Executable) || launch.Args[0] != "--noprofile" || launch.Args[1] != "--norc" {
		t.Fatalf("tool startup contract changed: %q %q", launch.Executable, launch.Args)
	}
	if !strings.HasSuffix(launch.Args[len(launch.Args)-1], "echo ok") {
		t.Fatalf("launch lost command: %q", launch.Args)
	}
	alias := filepath.Join(t.TempDir(), "cwd-link")
	if err := os.Symlink(opts.CWD, alias); err != nil {
		t.Fatal(err)
	}
	linkedOpts := opts
	linkedOpts.CWD = alias
	linked, err := ResolveTool(config.BashConfig{}, linkedOpts)
	if err != nil || linked.CWD != launch.CWD || linked.Binding.Fingerprint != launch.Binding.Fingerprint {
		t.Fatal("cwd identity must use the actual canonical directory", err)
	}
	opts.Command = "echo other"
	other, err := ResolveTool(config.BashConfig{}, opts)
	if err != nil || other.Binding.Fingerprint == launch.Binding.Fingerprint || other.Binding.ContractFingerprint != launch.Binding.ContractFingerprint {
		t.Fatal("command must change exact binding, not reusable shell contract", err)
	}
	opts.Env = append(opts.Env, "REVIEW_TEST=changed")
	changed, err := ResolveTool(config.BashConfig{}, opts)
	if err != nil || changed.Binding.ContractFingerprint == other.Binding.ContractFingerprint {
		t.Fatal("environment must change contract binding", err)
	}
	if changed.Binding.RuleFingerprint != other.Binding.RuleFingerprint {
		t.Fatal("environment changes must not narrow existing path-rule scope")
	}
	for _, args := range [][]string{
		{"-lc", "{{command}}"}, {"-i", "-c", "{{command}}"},
		{"-c", "echo hidden; {{command}}"}, {"-c", "{{command}}", "extra"},
	} {
		l, err := ResolveTool(config.BashConfig{ShellArgs: args}, opts)
		if err != nil || l.Binding.SyntaxSupported {
			t.Fatalf("unreviewed template %q: %+v %v", args, l.Binding, err)
		}
	}
	// A terminal Git Bash setting must never redirect tools to another shell.
	cfg := config.BashConfig{}
	cfg.GitBash.Enabled = true
	cfg.GitBash.RuntimeRoot = "/missing-bundle"
	independent, err := ResolveTool(cfg, opts)
	if err != nil || independent.GitBash || independent.Executable != launch.Executable {
		t.Fatal("terminal configuration changed tool executable", err)
	}
}

func TestSystemBashUpgradeRefreshesIdentityButInvalidatesGrants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix package-manager installation fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(version, body string) string {
		t.Helper()
		path := filepath.Join(root, version, "bash")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := install("version-one", "fixture one")
	link := filepath.Join(bin, "bash")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	search := []string{"PATH=" + bin}
	opts := Options{GOOS: runtime.GOOS, Command: "echo ok", CWD: root, Env: search}
	shells := captureToolSystemShells(search)
	before, err := resolveTool(config.BashConfig{}, opts, shells)
	if err != nil || !before.Binding.SyntaxSupported {
		t.Fatal("initial system installation was not recognized", err)
	}
	// apt-style replacement at the same path does not revoke grammar support.
	if err := os.WriteFile(old, []byte("upgraded bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := resolveTool(config.BashConfig{}, opts, shells)
	if err != nil || !after.Binding.SyntaxSupported || after.Binding.RuleFingerprint == before.Binding.RuleFingerprint || after.Binding.Fingerprint == before.Binding.Fingerprint {
		t.Fatal("replacement must preserve syntax and invalidate existing grants", err)
	}
	// Homebrew-style symlink retargeting must use the new canonical installation.
	newPath := install("version-two", "new installation")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newPath, link); err != nil {
		t.Fatal(err)
	}
	next, err := resolveTool(config.BashConfig{}, opts, shells)
	if err != nil || !next.Binding.SyntaxSupported || next.Executable == after.Executable || next.Binding.Fingerprint == after.Binding.Fingerprint {
		t.Fatal("symlink upgrade was not recognized without restart", err)
	}
	// A per-tool PATH is not allowed to redefine the system reference.
	opts.Env = []string{"PATH=" + filepath.Dir(old)}
	shadow, err := resolveTool(config.BashConfig{}, opts, shells)
	if err != nil || shadow.Binding.SyntaxSupported {
		t.Fatal("tool PATH changed the frozen search reference", err)
	}
}

func TestSystemBashIsTheStartupPathEntryNotALaterShadow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shadow shell fixture")
	}
	root := t.TempDir()
	early, system := filepath.Join(root, "writable"), filepath.Join(root, "system")
	for _, dir := range []string{early, system} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(system, "bash"), []byte("system"), 0o755); err != nil {
		t.Fatal(err)
	}
	search := []string{"PATH=" + early + string(os.PathListSeparator) + system}
	shells := captureToolSystemShells(search)
	opts := Options{GOOS: runtime.GOOS, Command: "echo ok", CWD: root, Env: search}
	if before, err := resolveTool(config.BashConfig{}, opts, shells); err != nil || !before.Binding.SyntaxSupported {
		t.Fatal("startup system Bash was not recognized", err)
	}
	// A same-name program created later in an earlier PATH directory runs
	// instead of the system Bash, so it must not inherit Bash analysis.
	if err := os.WriteFile(filepath.Join(early, "bash"), []byte("shadow"), 0o755); err != nil {
		t.Fatal(err)
	}
	shadow, _ := filepath.EvalSymlinks(filepath.Join(early, "bash"))
	after, err := resolveTool(config.BashConfig{}, opts, shells)
	if err != nil || after.Invocation.Executable != shadow || after.Binding.SyntaxSupported || after.Binding.Dialect != dialect.Bash {
		t.Fatalf("later PATH shadow was trusted as the system Bash: %+v %v", after, err)
	}
}

func TestToolLaunchDoesNotTrustShadowedShellOrUnstableEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shadow shell fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bash")
	marker := filepath.Join(dir, "executed")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{GOOS: runtime.GOOS, Command: "echo ok", CWD: dir, Env: []string{"PATH=" + dir}}
	launch, err := ResolveTool(config.BashConfig{}, opts)
	canonical, _ := filepath.EvalSymlinks(path)
	if err != nil || launch.Binding.SyntaxSupported || launch.Executable != canonical {
		t.Fatalf("shadow shell: %+v %v", launch.Binding, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shell resolution executed the configured program")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, err := ResolveTool(config.BashConfig{}, opts)
	if err != nil || changed.Binding.ContractFingerprint == launch.Binding.ContractFingerprint {
		t.Fatal("shell bytes must invalidate contract", err)
	}
	a := toolEnvironment([]string{"PATH=a", "PATH=b", "X=1"}, false)
	b := toolEnvironment([]string{"PATH=b", "PATH=a", "X=1"}, false)
	if strings.Join(a, "\x00") == strings.Join(b, "\x00") {
		t.Fatal("duplicate environment order must preserve its effective value")
	}
}

func TestWindowsToolGrammarIsNeverTreatedAsBash(t *testing.T) {
	for _, name := range []string{"powershell.exe", "pwsh.exe", "cmd.exe", "bash.exe"} {
		program, args := ToolInvocation(config.BashConfig{ShellExecutable: name}, "{{command}}", "windows")
		binding := classifyToolShell(program, program, args, "windows", toolSystemShells)
		if binding.SyntaxSupported || binding.Dialect == dialect.Bash {
			t.Fatalf("Windows shell %q incorrectly gained Bash analysis: %+v", name, binding)
		}
	}
}
