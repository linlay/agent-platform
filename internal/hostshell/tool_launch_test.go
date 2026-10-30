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
	if toolSystemShells["bash"].Path == "" {
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
		binding := classifyToolShell(program, program, "bytes", args, "windows")
		if binding.SyntaxSupported || binding.Dialect == dialect.Bash {
			t.Fatalf("Windows shell %q incorrectly gained Bash analysis: %+v", name, binding)
		}
	}
}
