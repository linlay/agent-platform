package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestMountedConnectorPathMatchesExecutionAndApprovalEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "connector-test-command"), []byte("#!/bin/sh\nprintf connector-ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := &contracts.ExecutionContext{Session: contracts.QuerySession{ConnectorBinDirs: []string{bin}}}
	env, err := mergeCommandEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", "connector-test-command")
	command.Env = env
	output, err := command.Output()
	if err != nil || string(output) != "connector-ok" {
		t.Fatalf("connector did not execute: %q %v", output, err)
	}
	if !strings.HasPrefix(bashEnvironmentVariables(env)["PATH"], bin+string(os.PathListSeparator)) {
		t.Fatal("review environment does not resolve mounted command first")
	}
	other, err := mergeCommandEnv(&contracts.ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bashEnvironmentVariables(other)["PATH"], bin) {
		t.Fatal("mounted command leaked to another agent")
	}
}
