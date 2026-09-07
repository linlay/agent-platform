package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	runtimetools "agent-platform/internal/tools"
)

// Opt-in acceptance test: uses the real Host Bash executor and a caller-supplied
// cli-mock binary, never the model/provider or an existing user's running chat.
// AP_TEST_MOCK_BINARY=/absolute/path/mock go test ./internal/llm -run '^TestHostBashApprovalConcurrentMockIntegration$' -v -count=1
func TestHostBashApprovalConcurrentMockIntegration(t *testing.T) {
	binary := os.Getenv("AP_TEST_MOCK_BINARY")
	if binary == "" {
		t.Skip("set AP_TEST_MOCK_BINARY to run the three 30-second Host Bash acceptance cases")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("mock binary must be absolute")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	for _, mode := range []string{"approve", "approve_rule_run", "auto_approve"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Config{Bash: config.BashConfig{AllowedCommands: []string{"echo"}, ShellFeaturesEnabled: true, MaxCommandChars: 16000}}
			executor, err := runtimetools.NewRuntimeToolExecutor(cfg, nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			session := QuerySession{RunID: "mock_" + mode, ChatID: "mock_acceptance", WorkspaceRoot: filepath.Dir(binary), AccessLevel: AccessLevelDefault}
			if mode == "auto_approve" {
				session.AccessLevel = AccessLevelAutoApprove
			}
			s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{cfg: cfg, tools: executor}, runControl: NewRunControl(ctx, session.RunID), execCtx: &ExecutionContext{Session: session, AccessLevel: session.AccessLevel, StartedAt: time.Now(), Budget: Budget{Tool: RetryPolicy{MaxCalls: 10, Timeout: 60}}}}
			s.runControl.SetInitialAccessLevel(session.AccessLevel)
			s.queuedToolCalls = []*preparedToolInvocation{
				{toolID: "long_run", toolName: "bash", args: map[string]any{"command": quote(binary) + " long-run --duration 30s", "cwd": filepath.Dir(binary), "timeout": 60}},
				{toolID: "qr_login", toolName: "bash", args: map[string]any{"command": quote(binary) + " qr-login --wait 30s", "cwd": filepath.Dir(binary), "timeout": 60}},
			}
			if mode != "auto_approve" {
				submitApprovedBashBatch(t, s, mode, mode)
			}
			started := time.Now()
			if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
				t.Fatal(err)
			}
			if s.activeToolBatch == nil {
				t.Fatal("mock calls did not enter concurrent batch")
			}
			firstOutput := map[string]time.Duration{}
			completed := map[string]time.Duration{}
			for s.activeToolBatch != nil {
				if err := s.consumeActiveToolBatch(); err != nil {
					t.Fatal(err)
				}
				for _, delta := range s.pending {
					switch v := delta.(type) {
					case DeltaToolOutput:
						if _, ok := firstOutput[v.ToolID]; !ok {
							firstOutput[v.ToolID] = time.Since(started)
							t.Logf("%s first process output: %s (+%s)", v.ToolID, time.Now().Format("15:04:05.000"), firstOutput[v.ToolID])
						}
					case DeltaToolResult:
						if v.Result.Error != "" || v.Result.ExitCode != 0 {
							t.Fatalf("mock failed: %#v", v.Result)
						}
						if _, ok := completed[v.ToolID]; ok {
							t.Fatal("duplicate result")
						}
						completed[v.ToolID] = time.Since(started)
						t.Logf("%s completed: %s (+%s)", v.ToolID, time.Now().Format("15:04:05.000"), completed[v.ToolID])
					}
				}
				s.pending = nil
			}
			elapsed := time.Since(started)
			if len(firstOutput) != 2 || len(completed) != 2 {
				t.Fatalf("missing process output/results: %v %v", firstOutput, completed)
			}
			for id, delay := range firstOutput {
				if delay > 5*time.Second {
					t.Fatalf("%s process did not produce early output: %s", id, delay)
				}
			}
			if elapsed < 29*time.Second || elapsed > 40*time.Second {
				t.Fatalf("unexpected total execution time %s (expected ~30s)", elapsed)
			}
			t.Logf("both 30-second commands completed in %s", elapsed)
		})
	}
}
