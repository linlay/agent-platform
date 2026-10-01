package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/hostenv"
)

func TestCommandEnvAppendsSkillPathAndHonorsInheritList(t *testing.T) {
	skillBin := t.TempDir()
	t.Setenv("AP_TEST_VISIBLE", "yes")
	t.Setenv("AP_TEST_SECRET", "no")
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{InheritEnv: []string{"PATH", "HOME", "AP_TEST_VISIBLE"}}}}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{PathAppend: []string{skillBin}}}
	env, err := executor.commandEnv(execCtx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.SplitList(hostenv.Value(env, "PATH"))
	if len(path) == 0 || path[len(path)-1] != skillBin {
		t.Fatalf("skill directory must be appended last: %v", path)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "AP_TEST_VISIBLE=yes") || strings.Contains(joined, "AP_TEST_SECRET") {
		t.Fatalf("inherit list not applied: %s", joined)
	}
	if strings.Contains(joined, "SSH_AUTH_SOCK=") && os.Getenv("SSH_AUTH_SOCK") != "" {
		t.Fatal("the SSH agent is not part of the ordinary tool environment")
	}
}
