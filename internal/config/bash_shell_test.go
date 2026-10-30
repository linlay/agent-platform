package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestShellExecutableRejectsRelativePathsAtLoad(t *testing.T) {
	for _, name := range []string{".", "..", "./bash", "../bin/bash", "bin/bash", `.\pwsh.exe`, `C:pwsh.exe`, `\pwsh.exe`, "bash\u202e"} {
		cfg := defaultConfig(LoadOptions{})
		cfg.Bash.ShellExecutable = name
		if err := cfg.normalize(t.TempDir()); err == nil || !strings.Contains(err.Error(), "bash.shell-executable") {
			t.Fatalf("invalid shell %q was not rejected at normalization: %v", name, err)
		}
	}
	valid := []string{"", "bash", "pwsh.exe", `C:\Program Files\PowerShell\7\pwsh.exe`, `\\server\share\pwsh.exe`}
	if runtime.GOOS != "windows" {
		valid = append(valid, "/bin/bash")
	}
	for _, name := range valid {
		if err := validateBashShellConfig(BashConfig{ShellExecutable: name}); err != nil {
			t.Fatalf("valid executable form %q: %v", name, err)
		}
	}
	if err := validateBashShellConfig(BashConfig{ShellArgs: []string{"-c", "echo\x1b[2K {{command}}"}}); err == nil {
		t.Fatal("unsafe startup text accepted")
	}
}
