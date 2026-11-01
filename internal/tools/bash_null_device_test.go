package tools

import (
	"agent-platform/internal/shellreview/resources"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt in only with an available disposable Linux image. This runs the exact
// production probe as an unprivileged uid; it executes no reviewed user source.
func TestSandboxNullProbeAgainstLinuxImage(t *testing.T) {
	image := os.Getenv("AP_SHELL_STAT_TEST_IMAGE")
	if image == "" {
		t.Skip("set AP_SHELL_STAT_TEST_IMAGE for real Linux image validation")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--user", "65534:65534", "--entrypoint", "/bin/bash", image, "--noprofile", "--norc", "-c", sandboxNullDeviceProbeCommand)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("immutable probe failed: %v", err)
	}
	result, err := resources.ParseLinuxNullDevice(string(output))
	if err != nil || !result.NullDevice {
		t.Fatalf("actual Linux stat response %q: %+v %v", output, result, err)
	}
	cmd = exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--user", "0:0", "--entrypoint", "/bin/bash", image, "--noprofile", "--norc", "-c", sandboxNullDeviceProbeCommand)
	if output, err := cmd.Output(); err == nil || len(output) != 0 {
		t.Fatalf("root-writable stat was started: %q %v", output, err)
	}
}

func TestSandboxConfigProbeAgainstLinuxImage(t *testing.T) {
	image := os.Getenv("AP_SHELL_STAT_TEST_IMAGE")
	if image == "" {
		t.Skip("set AP_SHELL_STAT_TEST_IMAGE for real Linux validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target := "/tmp/config space'$(printf unsafe).rc"
	content := "fixture config\n"
	script := "printf '%s' " + shellQuoteProbe(content) + " > " + shellQuoteProbe(target) + "; " + sandboxConfigProbeCommand(target)
	run := func(uid, command string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--user", uid, "--entrypoint", "/bin/bash", image, "--noprofile", "--norc", "-c", command).Output()
	}
	raw, err := run("65534:65534", script)
	if err != nil {
		t.Fatalf("config probe failed: %v", err)
	}
	info, err := parseConfigProbe(string(raw), target)
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	if err != nil || !info.Exists || info.Canonical != target || info.SHA256 != expected {
		t.Fatalf("quoted config probe: %+v %v", info, err)
	}
	raw, err = run("65534:65534", sandboxConfigProbeCommand("/tmp/absent/fixture.rc"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := parseConfigProbe(string(raw), "/tmp/absent/fixture.rc"); err != nil || info.Exists {
		t.Fatalf("missing config: %+v %v", info, err)
	}
	raw, err = run("0:0", sandboxConfigProbeCommand("/tmp/absent/fixture.rc"))
	if err == nil || len(raw) != 0 {
		t.Fatalf("writable utility profile manufactured missing proof: %q %v", raw, err)
	}
	raw, err = run("65534:65534", immutableDependencyTools("getent")+accountHomeProbeBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccountHomeProbe(string(raw)); err != nil {
		t.Fatalf("actual account protocol: %v", err)
	}
}
