package hostshell

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestManagedInvocationAndEnvironment(t *testing.T) {
	root, temp := t.TempDir(), t.TempDir()
	cfg := config.BashConfig{ShellExecutable: "ignored.exe", ShellArgs: []string{"ignored"}, GitBash: config.GitBashConfig{Enabled: true, RuntimeRoot: root}}
	base := []string{`Path=C:\Windows\System32`, "apiPayload={\"path\":\"/v1/test\"}", "BASH_ENV=untrusted", "MSYS2_ARG_CONV_EXCL=bad", "AP_GIT_BASH_EXE=untrusted"}
	for _, interactive := range []bool{false, true} {
		inv, err := Resolve(cfg, Options{GOOS: "windows", Command: "printf '中文'", Interactive: interactive, Env: base, TempDir: temp, CWD: temp})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"--noprofile", "--norc", "-o", "pipefail", "-c", "printf '中文'"}
		if interactive {
			want = []string{"--noprofile", "--norc", "-i"}
		}
		if !reflect.DeepEqual(inv.Args, want) || inv.Executable != filepath.Join(root, "usr", "bin", "bash.exe") || inv.CWD != temp {
			t.Fatalf("invocation: %+v", inv)
		}
		if envValue(inv.Env, "BASH_ENV") != "" || envValue(inv.Env, "MSYS2_ARG_CONV_EXCL") != "*" || envValue(inv.Env, "AP_GIT_BASH_EXE") != inv.Executable {
			t.Fatalf("uncontrolled environment: %v", inv.Env)
		}
		if !strings.Contains(strings.Join(inv.Env, "\n"), "apiPayload={\"path\":\"/v1/test\"}") {
			t.Fatal("custom variable name/value changed")
		}
		if envValue(inv.Env, "TMPDIR") != temp {
			t.Fatal("temporary root changed")
		}
		if !strings.Contains(envValue(inv.Env, "MSYS2_ENV_CONV_EXCL"), "apiPayload=") || strings.Contains(envValue(inv.Env, "MSYS2_ENV_CONV_EXCL"), "PATH=") {
			t.Fatal("conversion exclusions incorrect")
		}
	}
}

func TestManagedUnavailableAndLegacyFallback(t *testing.T) {
	cfg := config.BashConfig{GitBash: config.GitBashConfig{Enabled: true}}
	if _, err := Resolve(cfg, Options{GOOS: "windows"}); err == nil {
		t.Fatal("missing runtime silently accepted")
	}
	inv, err := Resolve(cfg, Options{GOOS: "linux", Command: "true", Env: []string{"AP_GIT_BASH_EXE=leaked"}})
	if err != nil || inv.GitBash || inv.Executable != "bash" || envValue(inv.Env, "AP_GIT_BASH_EXE") != "" {
		t.Fatalf("Unix behavior: %+v %v", inv, err)
	}
	cfg.GitBash.Enabled = false
	cfg.ShellExecutable = "pwsh.exe"
	inv, err = Resolve(cfg, Options{GOOS: "windows", Command: "echo ok"})
	if err != nil || inv.Executable != "pwsh.exe" || inv.GitBash {
		t.Fatalf("rollback: %+v %v", inv, err)
	}
}

func TestCommandBaseWindowsSuffix(t *testing.T) {
	if CommandBase(`C:\tools\python.exe`, true) != "python" || CommandBase("python.exe", false) != "python.exe" {
		t.Fatal("platform-specific command family")
	}
}
