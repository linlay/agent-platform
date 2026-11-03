package hostshell

import (
	"agent-platform/internal/config"
	"reflect"
	"testing"
)

func TestResolveHostShellInvocationDefaultsToPowerShellOnWindows(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{}, "Get-Process", "windows")

	if executable != "powershell.exe" {
		t.Fatalf("expected powershell.exe, got %q", executable)
	}
	wantCommand := "$OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false; [Console]::OutputEncoding = $OutputEncoding; Get-Process"
	wantArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", wantCommand}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationDefaultsToUTF8CmdOnWindows(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{
		ShellExecutable: "cmd.exe",
	}, "dir", "windows")

	if executable != "cmd.exe" {
		t.Fatalf("expected cmd.exe, got %q", executable)
	}
	wantArgs := []string{"/d", "/s", "/c", "chcp 65001 >NUL & dir"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationDefaultsToBashOnUnix(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{}, "pwd", "linux")

	if executable != "bash" {
		t.Fatalf("expected bash, got %q", executable)
	}
	wantArgs := []string{"--noprofile", "--norc", "-o", "pipefail", "-c", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationLeavesNonBashUnixDefaultsUnchanged(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{
		ShellExecutable: "sh",
	}, "pwd", "linux")

	if executable != "sh" {
		t.Fatalf("expected sh, got %q", executable)
	}
	wantArgs := []string{"-c", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationCustomBashArgsRemainAuthoritative(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{
		ShellExecutable: "bash",
		ShellArgs:       []string{"-lc", "{{command}}"},
	}, "pwd", "linux")

	if executable != "bash" {
		t.Fatalf("expected bash, got %q", executable)
	}
	wantArgs := []string{"-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationSupportsCustomArgs(t *testing.T) {
	executable, args := ToolInvocation(config.BashConfig{
		ShellExecutable: "cmd.exe",
		ShellArgs:       []string{"/d", "/s", "/c", "{{command}}"},
	}, "dir", "windows")

	if executable != "cmd.exe" {
		t.Fatalf("expected cmd.exe, got %q", executable)
	}
	wantArgs := []string{"/d", "/s", "/c", "dir"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationAppendsCommandWithoutPlaceholder(t *testing.T) {
	_, args := ToolInvocation(config.BashConfig{
		ShellExecutable: "pwsh.exe",
		ShellArgs:       []string{"-NoProfile", "-Command"},
	}, "node --version", "windows")

	wantArgs := []string{"-NoProfile", "-Command", "node --version"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}
