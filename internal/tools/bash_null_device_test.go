package tools

import (
	"agent-platform/internal/shellreview/resources"
	"context"
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
