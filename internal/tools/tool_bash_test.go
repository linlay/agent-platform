package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	contracts "agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
)

type collectingToolOutputSink struct {
	chunks chan contracts.ToolOutput
}

func TestInvokeHostBashCancellationPreservesPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := "echo started; while :; do :; done"
	if runtime.GOOS == "windows" {
		command = "Write-Output started; Start-Sleep -Seconds 30"
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{
		AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true, MaxCommandChars: 16000,
	}}}
	execCtx := bashExecutionContext(t.TempDir())
	execCtx.Session.AccessLevel = contracts.AccessLevelFullAccess
	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	execCtx.ToolOutputSink = sink
	resultCh := make(chan contracts.ToolExecutionResult, 1)
	go func() {
		result, err := executor.invokeHostBash(ctx, map[string]any{"command": command}, execCtx)
		if err != nil {
			result.Error = err.Error()
		}
		resultCh <- result
	}()
	select {
	case <-sink.chunks:
	case result := <-resultCh:
		t.Fatalf("Bash exited before cancellation: %#v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("Bash produced no startup output")
	}
	cancel()
	select {
	case result := <-resultCh:
		if result.ExitCode == 0 || !strings.Contains(result.Output, "started") {
			t.Fatalf("canceled Bash lost failure/partial output: %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bash cancellation did not return within the Run cleanup bound")
	}
}

func (s *collectingToolOutputSink) EmitToolOutput(ctx context.Context, output contracts.ToolOutput) error {
	select {
	case s.chunks <- output:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestResolveHostShellInvocationDefaultsToPowerShellOnWindows(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{}, "Get-Process", "windows")

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
	executable, args := resolveHostShellInvocation(config.BashConfig{
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
	executable, args := resolveHostShellInvocation(config.BashConfig{}, "pwd", "linux")

	if executable != "bash" {
		t.Fatalf("expected bash, got %q", executable)
	}
	wantArgs := []string{"-o", "pipefail", "-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationLeavesNonBashUnixDefaultsUnchanged(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "sh",
	}, "pwd", "linux")

	if executable != "sh" {
		t.Fatalf("expected sh, got %q", executable)
	}
	wantArgs := []string{"-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationCustomBashArgsRemainAuthoritative(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
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
	executable, args := resolveHostShellInvocation(config.BashConfig{
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
	_, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "pwsh.exe",
		ShellArgs:       []string{"-NoProfile", "-Command"},
	}, "node --version", "windows")

	wantArgs := []string{"-NoProfile", "-Command", "node --version"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestInvokeHostBashSuccessReturnsPlainStdout(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"echo"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "echo hello"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Output != "hello\n" {
		t.Fatalf("expected raw stdout, got %q", result.Output)
	}
	if result.Structured != nil {
		t.Fatalf("expected nil structured result, got %#v", result.Structured)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Error != "" {
		t.Fatalf("expected empty error, got %q", result.Error)
	}
}

func TestInvokeHostBashStreamsOutputBeforeCompletion(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf", "sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	execCtx := bashExecutionContext(root)
	execCtx.ToolOutputSink = sink
	resultCh := make(chan contracts.ToolExecutionResult, 1)
	go func() {
		result, _ := executor.invokeHostBash(context.Background(), map[string]any{
			"command": "printf 'scan-qr\\n'; sleep 0.25; printf 'done\\n'",
		}, execCtx)
		resultCh <- result
	}()

	select {
	case chunk := <-sink.chunks:
		if chunk.Stream != contracts.ToolOutputStdout || chunk.Delta != "scan-qr\n" {
			t.Fatalf("unexpected first live chunk: %#v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive live output while bash was running")
	}
	select {
	case result := <-resultCh:
		t.Fatalf("bash completed before the live output was observed: %#v", result)
	default:
	}
	result := <-resultCh
	if result.Output != "scan-qr\ndone\n" || result.ExitCode != 0 {
		t.Fatalf("unexpected final result: %#v", result)
	}
}

func TestInvokeHostBashStreamsCarriageReturnUpdates(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf", "sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 16)}
	execCtx := bashExecutionContext(root)
	execCtx.ToolOutputSink = sink
	resultCh := make(chan contracts.ToolExecutionResult, 1)
	go func() {
		result, _ := executor.invokeHostBash(context.Background(), map[string]any{
			"command": "printf '\\rprogress=000%%'; sleep 0.12; printf '\\rprogress=050%%'; sleep 0.12; printf '\\033[2K\\rprogress=100%%\\n'",
		}, execCtx)
		resultCh <- result
	}()

	var first contracts.ToolOutput
	select {
	case first = <-sink.chunks:
	case <-time.After(time.Second):
		t.Fatal("did not receive the initial carriage-return progress chunk")
	}
	if first.Stream != contracts.ToolOutputStdout || first.Delta != "\rprogress=000%" {
		t.Fatalf("unexpected first progress chunk: %#v", first)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("bash completed before progress updates were streamed: %#v", result)
	default:
	}

	result := <-resultCh
	want := "\rprogress=000%\rprogress=050%\x1b[2K\rprogress=100%\n"
	if result.Output != want || result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("unexpected final carriage-return result: %#v", result)
	}
	var live strings.Builder
	live.WriteString(first.Delta)
	for len(sink.chunks) > 0 {
		chunk := <-sink.chunks
		if chunk.Stream != contracts.ToolOutputStdout {
			t.Fatalf("unexpected progress stream: %#v", chunk)
		}
		live.WriteString(chunk.Delta)
	}
	if live.String() != want {
		t.Fatalf("live carriage-return output %q, want %q", live.String(), want)
	}
}

func TestInvokeHostBashWithSinkAndNoOutputEmitsNoChunks(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{
		AllowedCommands: []string{"true"}, ShellFeaturesEnabled: true, ShellExecutable: "bash", MaxCommandChars: 16000,
	}}}
	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 1)}
	execCtx := bashExecutionContext(root)
	execCtx.ToolOutputSink = sink
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "true"}, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("unexpected no-output bash result: result=%#v err=%v", result, err)
	}
	select {
	case chunk := <-sink.chunks:
		t.Fatalf("no-output command emitted live chunk: %#v", chunk)
	default:
	}
}

func TestBashLiveOutputEncoderPreservesSplitUTF8AndReplacesInvalidBytes(t *testing.T) {
	first, consumed := encodeBashLiveOutputPrefix([]byte{0xe4, 0xbd}, false, bashLiveOutputMaxChunkBytes)
	if first != "" || consumed != 0 {
		t.Fatalf("incomplete UTF-8 must remain buffered, got %q consumed=%d", first, consumed)
	}
	encoded, consumed := encodeBashLiveOutputPrefix([]byte{0xe4, 0xbd, 0xa0, 0xff, 'x'}, false, bashLiveOutputMaxChunkBytes)
	if encoded != "你\uFFFDx" || consumed != 5 {
		t.Fatalf("unexpected live UTF-8 encoding: %q consumed=%d", encoded, consumed)
	}
}

func TestInvokeHostBashPipefailPreservesUpstreamFailure(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"false", "tail"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{
		"command": "false | tail -200",
	}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("expected pipefail to preserve upstream exit code 1, got %#v", result)
	}
	if result.Structured == nil || result.Structured["exitCode"] != 1 {
		t.Fatalf("expected structured pipeline failure, got %#v", result)
	}
}

func TestInvokeHostBashSuccessWithStderrReturnsStructuredJSON(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "emit.sh")
	if err := os.WriteFile(scriptPath, []byte("printf 'warn\\n' >&2\nprintf 'ok\\n'\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			AccessPolicy: config.AccessPolicyConfig{
				Levels: map[string]config.AccessPolicyLevelConfig{
					contracts.AccessLevelDefault: {
						Approvals: config.AccessPolicyApprovalConfig{
							BashOpaqueCommand: "allow",
						},
					},
				},
			},
			Bash: config.BashConfig{
				AllowedCommands: []string{"sh"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	execCtx := bashExecutionContext(root)
	execCtx.ToolOutputSink = sink
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "sh " + scriptPath}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Structured == nil {
		t.Fatal("expected structured result when stderr is present")
	}
	if result.Structured["stdout"] != "ok\n" {
		t.Fatalf("expected stdout to stay separate, got %#v", result.Structured)
	}
	if result.Structured["stderr"] != "warn\n" {
		t.Fatalf("expected stderr to be preserved, got %#v", result.Structured)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if payload["stderr"] != "warn\n" {
		t.Fatalf("expected marshaled stderr to be preserved, got %#v", payload)
	}
	seenStreams := map[string]string{}
	for len(sink.chunks) > 0 {
		chunk := <-sink.chunks
		seenStreams[chunk.Stream] += chunk.Delta
	}
	if seenStreams[contracts.ToolOutputStdout] != "ok\n" || seenStreams[contracts.ToolOutputStderr] != "warn\n" {
		t.Fatalf("expected separated live stdout/stderr, got %#v", seenStreams)
	}
}

func TestInvokeHostBashDoesNotWaitForBackgroundProcessOutput(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"echo", "sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	execCtx := bashExecutionContext(root)
	execCtx.ToolOutputSink = sink
	start := time.Now()
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "sleep 2 & echo done"}, execCtx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("expected background command to return quickly, took %s", elapsed)
	}
	if result.Output != "done\n" {
		t.Fatalf("expected raw stdout from shell command, got %q", result.Output)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	select {
	case chunk := <-sink.chunks:
		if chunk.Stream != contracts.ToolOutputStdout || chunk.Delta != "done\n" {
			t.Fatalf("unexpected live background-process chunk: %#v", chunk)
		}
	default:
		t.Fatal("expected live output before returning from background command")
	}
}

func TestInvokeHostBashDefaultsTimeoutToToolBudget(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf", "sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	start := time.Now()
	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	execCtx := &contracts.ExecutionContext{
		Session:        contracts.QuerySession{WorkspaceRoot: root},
		Budget:         contracts.Budget{Tool: contracts.RetryPolicy{Timeout: 1}},
		ToolOutputSink: sink,
	}
	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{"command": "printf 'before-timeout\\n'; sleep 2"},
		execCtx,
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.ExitCode != -1 || !strings.Contains(result.Output, "Command timed out") {
		t.Fatalf("expected command timeout result, got %#v", result)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("expected budget timeout near 1s, took %s", elapsed)
	}
	select {
	case chunk := <-sink.chunks:
		if chunk.Stream != contracts.ToolOutputStdout || chunk.Delta != "before-timeout\n" {
			t.Fatalf("unexpected live timeout chunk: %#v", chunk)
		}
	default:
		t.Fatal("expected output emitted before timeout")
	}
}

func TestResolveBashTimeoutCapsRequestedTimeoutAtToolBudget(t *testing.T) {
	executor := &RuntimeToolExecutor{}
	execCtx := &contracts.ExecutionContext{Budget: contracts.Budget{Tool: contracts.RetryPolicy{Timeout: 5}}}

	if got := executor.resolveBashTimeoutSeconds(map[string]any{}, execCtx); got != 5 {
		t.Fatalf("default bash timeout = %d, want tool budget 5", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{"timeout": 2}, execCtx); got != 2 {
		t.Fatalf("short requested bash timeout = %d, want 2", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{"timeout": 10}, execCtx); got != 5 {
		t.Fatalf("capped requested bash timeout = %d, want tool budget 5", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{}, nil); got != defaultBashTimeoutSeconds {
		t.Fatalf("fallback bash timeout = %d, want %d", got, defaultBashTimeoutSeconds)
	}
}

func TestInvokeHostBashDefaultsCwdToSessionWorkspace(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{WorkspaceRoot: root}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "pwd"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if strings.TrimSpace(result.Output) != want {
		t.Fatalf("expected pwd in workspace %q, got %q", want, result.Output)
	}
}

func TestInvokeHostBashWithoutWorkspaceRequiresExplicitCwd(t *testing.T) {
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{"command": "pwd"},
		&contracts.ExecutionContext{Session: contracts.QuerySession{}},
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "workspace_unavailable" ||
		!strings.Contains(result.Output, "pass cwd explicitly") ||
		!strings.Contains(result.Output, "@chat") {
		t.Fatalf("expected actionable workspace_unavailable result, got %#v", result)
	}
}

func TestInvokeHostBashWithoutWorkspaceAllowsExplicitChatAndAgentCwd(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(root, "chats", "chat-1")
	agentDir := filepath.Join(root, "agents", "bootstrap")
	for _, dir := range []string{chatDir, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: chatDir, AgentDir: agentDir},
		},
	}}

	for alias, want := range map[string]string{"@chat": chatDir, "@agent": agentDir} {
		result, err := executor.invokeHostBash(
			context.Background(),
			map[string]any{"command": "pwd", "cwd": alias},
			execCtx,
		)
		if err != nil {
			t.Fatalf("invokeHostBash(%s) returned error: %v", alias, err)
		}
		want, _ = filepath.EvalSymlinks(want)
		if result.Error != "" || strings.TrimSpace(result.Output) != want {
			t.Fatalf("expected pwd in %s %q, got %#v", alias, want, result)
		}
	}
}

func TestInvokeHostBashFailureReturnsStructuredJSON(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"ls"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "ls missing"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Structured == nil {
		t.Fatal("expected structured failure result")
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code, got %#v", result)
	}
	if got, ok := result.Structured["exitCode"].(int); !ok || got != result.ExitCode {
		t.Fatalf("expected structured exit code %d, got %#v", result.ExitCode, result.Structured["exitCode"])
	}
	if got, _ := result.Structured["stderr"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("expected stderr metadata, got %#v", result.Structured)
	}
	if got, _ := result.Structured["stdout"].(string); got != "" {
		t.Fatalf("expected stdout to stay separate from stderr, got %#v", result.Structured)
	}
	if got, _ := result.Structured["stderr"].(string); !strings.Contains(got, "missing") {
		t.Fatalf("expected stderr to include command output, got %#v", result.Structured)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if _, ok := payload["stderr"]; !ok {
		t.Fatalf("expected stderr in marshaled output, got %#v", payload)
	}
}

func TestInvokeHostBashEarlyReturnStaysHumanReadable(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"echo"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat secret.txt"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "command_not_allowed" {
		t.Fatalf("expected command_not_allowed, got %#v", result)
	}
	if result.Structured != nil {
		t.Fatalf("expected nil structured result for early return, got %#v", result.Structured)
	}
	if !strings.Contains(result.Output, "Command not allowed: cat") {
		t.Fatalf("expected human-readable rejection, got %q", result.Output)
	}
}

func TestInvokeHostBashSupportsPerCallCwd(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"env"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "pwd",
			"cwd":     nested,
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	resolvedNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	got := strings.TrimSpace(result.Output)
	if got != nested && got != resolvedNested {
		t.Fatalf("expected cwd line to match %q or %q, got %q", nested, resolvedNested, got)
	}
}

func TestInvokeHostBashAllowsShellSyntaxByDefault(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"pwd", "cd"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "cd nested && pwd",
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	resolvedNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	got := strings.TrimSpace(result.Output)
	if got != nested && got != resolvedNested {
		t.Fatalf("expected shell syntax command to resolve nested cwd, got %q", got)
	}
}

func TestInvokeHostBashAllowsExitStatusSpecialParameter(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"false", "echo"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": `false; echo "Exit code: $?"`,
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected command to execute, got %#v", result)
	}
	if strings.TrimSpace(result.Output) != "Exit code: 1" {
		t.Fatalf("expected shell to preserve real exit status, got %q", result.Output)
	}
}

func TestInvokeHostBashIgnoresPerCallEnv(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"bash"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "env",
			"env":     map[string]any{"TEST_HOST_ENV": "call-value"},
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if strings.Contains(result.Output, "TEST_HOST_ENV=call-value") {
		t.Fatalf("expected host per-call env to be ignored, got %q", result.Output)
	}
}

func TestInvokeHostBashAppliesAgentEnvOverrides(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"bash"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "echo \"$TEST_HOST_ENV\"",
		},
		&contracts.ExecutionContext{
			Session:          contracts.QuerySession{WorkspaceRoot: root},
			StaticRuntimeEnv: map[string]string{"TEST_HOST_ENV": "agent-value"},
		},
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if strings.TrimSpace(result.Output) != "agent-value" {
		t.Fatalf("expected agent env override to apply, got %q", result.Output)
	}
}

func TestMergeCommandEnvInjectsReservedAgentAndChatContextAfterRuntimeOverrides(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents", "reader")
	chatDir := filepath.Join(root, "chats", "chat-1")
	t.Setenv("AP_AGENT_CONFIG_HOME", "/process-config")
	t.Setenv("AP_CHAT_DIR", "/process-chat")
	valuesFor := func(env []string) map[string]string {
		t.Helper()
		values := map[string]string{}
		for _, item := range env {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				values[key] = value
			}
		}
		return values
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			WorkspaceRoot: root,
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{AgentDir: agentDir, WorkspaceDir: root, ChatDir: chatDir},
			},
		},
	}
	if got, want := valuesFor(mustMergeCommandEnv(t, execCtx))["AP_AGENT_CONFIG_HOME"], filepath.Join(agentDir, ".config"); got != want {
		t.Fatalf("default AP_AGENT_CONFIG_HOME = %q, want %q", got, want)
	}
	if got := valuesFor(mustMergeCommandEnv(t, execCtx))["AP_CHAT_DIR"]; got != chatDir {
		t.Fatalf("default AP_CHAT_DIR = %q, want %q", got, chatDir)
	}
	if got := valuesFor(mustMergeCommandEnv(t, execCtx))["AP_WORKSPACE_DIR"]; got != root {
		t.Fatalf("default AP_WORKSPACE_DIR = %q, want %q", got, root)
	}
	execCtx.StaticRuntimeEnv = map[string]string{
		"AP_AGENT_CONFIG_HOME": "/agent-custom",
		"AP_WORKSPACE_DIR":     "/wrong-workspace",
		"AP_CHAT_DIR":          "/wrong-chat",
	}
	got := valuesFor(mustMergeCommandEnv(t, execCtx))
	if want := filepath.Join(agentDir, ".config"); got["AP_AGENT_CONFIG_HOME"] != want {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want reserved value %q", got["AP_AGENT_CONFIG_HOME"], want)
	}
	if got["AP_CHAT_DIR"] != chatDir {
		t.Fatalf("AP_CHAT_DIR = %q, want reserved value %q", got["AP_CHAT_DIR"], chatDir)
	}
	if got["AP_WORKSPACE_DIR"] != root {
		t.Fatalf("AP_WORKSPACE_DIR = %q, want reserved value %q", got["AP_WORKSPACE_DIR"], root)
	}
}

func TestMergeBashCommandEnvReadsCurrentIdentityTokenAndRejectsOverrides(t *testing.T) {
	identityFile := filepath.Join(t.TempDir(), "desktop state", "sso-access-token.txt")
	if err := os.MkdirAll(filepath.Dir(identityFile), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_ACCESS_TOKEN", "ambient-token")
	valuesFor := func(env []string) map[string]string {
		t.Helper()
		values := map[string]string{}
		for _, item := range env {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				values[key] = value
			}
		}
		return values
	}
	execCtx := &contracts.ExecutionContext{
		StaticRuntimeEnv: map[string]string{"AP_ACCESS_TOKEN": "runtime-token"},
	}

	if _, ok := valuesFor(mustMergeBashCommandEnv(t, execCtx, identityFile))[agentconfig.EnvAccessToken]; ok {
		t.Fatal("missing identity file must remove inherited and runtime access tokens")
	}
	if err := os.WriteFile(identityFile, []byte("token-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := valuesFor(mustMergeBashCommandEnv(t, execCtx, identityFile))[agentconfig.EnvAccessToken]; got != "token-a" {
		t.Fatalf("AP_ACCESS_TOKEN = %q, want token-a", got)
	}
	if err := os.WriteFile(identityFile, []byte("token-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := valuesFor(mustMergeBashCommandEnv(t, execCtx, identityFile))[agentconfig.EnvAccessToken]; got != "token-b" {
		t.Fatalf("AP_ACCESS_TOKEN = %q, want token-b", got)
	}
	if err := os.Remove(identityFile); err != nil {
		t.Fatal(err)
	}
	if _, ok := valuesFor(mustMergeBashCommandEnv(t, execCtx, identityFile))[agentconfig.EnvAccessToken]; ok {
		t.Fatal("removed identity file must remove AP_ACCESS_TOKEN from the next Host Bash")
	}
}

func TestInvokeHostBashInjectsCurrentDefaultIdentityToken(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	identityFile := filepath.Join(runtimeRoot, "identity", "access-token")
	if err := os.MkdirAll(filepath.Dir(identityFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityFile, []byte("current-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_RUNTIME_DIR", runtimeRoot)
	cfg, err := config.Load(config.LoadOptions{ConfigDir: root})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.IdentityFile != identityFile {
		t.Fatalf("identity file = %q, want default %q", cfg.IdentityFile, identityFile)
	}
	cfg.Bash = config.BashConfig{
		AllowedCommands: []string{"printenv"},
		ShellExecutable: "bash",
		MaxCommandChars: 16000,
	}
	executor := &RuntimeToolExecutor{
		cfg: cfg,
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{"command": "printenv AP_ACCESS_TOKEN"},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.ExitCode != 0 || result.Output != "current-token\n" {
		t.Fatalf("Host Bash did not receive current identity token: %#v", result)
	}
}

func TestInvokeHostBashSoftSecurityRequiresApproval(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "printf ok > owner.md"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_approval_required" {
		t.Fatalf("expected bash_security_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashConsumesMatchingSoftSecurityApproval(t *testing.T) {
	root := t.TempDir()
	command := "printf ok > owner.md"
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint(command): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected approved command to execute, got %#v", result)
	}
	if _, ok := execCtx.BashSecurityApprovals[bashsec.ApprovalFingerprint(command)]; ok {
		t.Fatalf("expected approval to be consumed, got %#v", execCtx.BashSecurityApprovals)
	}
	data, err := os.ReadFile(filepath.Join(root, "owner.md"))
	if err != nil {
		t.Fatalf("read owner.md: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("expected written content, got %q", string(data))
	}
}

func TestInvokeHostBashRejectsMismatchedSoftSecurityApproval(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint("printf ok > other.md"): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "printf ok > owner.md"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_approval_required" {
		t.Fatalf("expected bash_security_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashAccessPolicyRequiresApprovalForOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"cat"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat " + secret}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_access_approval_required" {
		t.Fatalf("expected bash_access_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashAutoApprovedAccessAddsMetadata(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"cat"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat " + secret}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected bash success, got %#v", result)
	}
	if result.Output != "secret\n" {
		t.Fatalf("expected stdout to stay plain, got %q", result.Output)
	}
	meta, _ := result.Structured["accessPolicy"].(map[string]any)
	if meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove {
		t.Fatalf("expected auto approval metadata, got %#v", result.Structured["accessPolicy"])
	}
}

func TestInvokeHostBashAppliesChatAndTempScriptApprovalRules(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chatDir := filepath.Join(root, "chats", "chat-1")
	tempRoot := filepath.Join(root, "temp")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{workspace, chatDir, tempRoot, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"python3", "node"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: workspace,
		ChatRoot:      chatDir,
		TempRoot:      tempRoot,
		TempRoots:     []string{tempRoot},
		RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{
			WorkspaceDir: workspace,
			ChatDir:      chatDir,
		}},
	}}

	tests := []struct {
		name        string
		interpreter string
		cwd         string
		script      string
		accessLevel string
		expectAudit bool
	}{
		{name: "auto-approved chat python", interpreter: "python3", cwd: "@chat", script: "task.py", accessLevel: contracts.AccessLevelAutoApprove, expectAudit: true},
		{name: "default temp node", interpreter: "node", cwd: "@temp", script: "task.js", accessLevel: contracts.AccessLevelDefault},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interpreter, err := exec.LookPath(test.interpreter)
			if err != nil {
				t.Skip("interpreter unavailable: ", err)
			}
			dir, content := tempRoot, "console.log('ran:"+test.script+"')\n"
			if test.interpreter == "python3" {
				dir = chatDir
				content = "print('ran:" + test.script + "')\n"
			}
			if err := os.WriteFile(filepath.Join(dir, test.script), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			command := "'" + interpreter + "' " + test.script
			execCtx.Session.AccessLevel = test.accessLevel
			result, err := executor.invokeHostBash(context.Background(), map[string]any{
				"command": command,
				"cwd":     test.cwd,
			}, execCtx)
			if err != nil {
				t.Fatalf("invokeHostBash returned error: %v", err)
			}
			if result.Error != "" || result.ExitCode != 0 || result.Output != "ran:"+test.script+"\n" {
				t.Fatalf("unexpected script result: %#v", result)
			}
			meta, hasAudit := result.Structured["accessPolicy"].(map[string]any)
			if test.expectAudit {
				if !hasAudit || meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove ||
					!strings.HasPrefix(fmt.Sprint(meta["ruleKey"]), "bash-access:opaque:") {
					t.Fatalf("expected opaque auto approval metadata, got %#v", result.Structured["accessPolicy"])
				}
			} else if hasAudit && meta["decision"] != "allow" {
				t.Fatalf("temporary script allow must not be recorded as an auto approval: %#v", meta)
			}
		})
	}
}

func TestInvokeHostBashAutoApprovedReadWithDevNullRedirection(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	note := filepath.Join(outside, "note.md")
	if err := os.WriteFile(note, []byte("# note\n"), 0o644); err != nil {
		t.Fatalf("write outside note: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	command := "find " + outside + ` -maxdepth 1 -name "*.md" -type f 2>/dev/null`
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected bash success, got %#v", result)
	}
	if strings.TrimSpace(result.Output) != note {
		t.Fatalf("expected note path in stdout, got %q", result.Output)
	}
	meta, _ := result.Structured["accessPolicy"].(map[string]any)
	if meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove {
		t.Fatalf("expected auto approval metadata, got %#v", result.Structured["accessPolicy"])
	}
}

func TestInvokeHostBashRealOutsideRedirectionStillRequiresAccessApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	logPath := filepath.Join(outside, "out.log")
	command := "printf ok > " + logPath
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			AccessLevel:   contracts.AccessLevelAutoApprove,
			WorkspaceRoot: root,
		},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint(command): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_access_approval_required" {
		t.Fatalf("expected bash_access_approval_required, got %#v", result)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("did not expect redirected file to be written, stat err=%v", err)
	}
}

func TestInvokeHostBashFullAccessStillKeepsBashsecHardBlock(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelFullAccess,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "eval echo hi"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_blocked" {
		t.Fatalf("expected bash_security_blocked, got %#v", result)
	}
}

func TestBashResultHardErrorReturnsStructuredJSON(t *testing.T) {
	result := bashResult("partial output", "runtime exploded", "host", "/tmp/work", 0, "sandbox_execute_failed")

	if result.Structured == nil {
		t.Fatal("expected structured failure result")
	}
	if result.Error != "sandbox_execute_failed" {
		t.Fatalf("expected helper to keep hard error, got %#v", result)
	}
	if result.Structured["error"] != "sandbox_execute_failed" {
		t.Fatalf("expected error in payload, got %#v", result.Structured)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if payload["error"] != "sandbox_execute_failed" {
		t.Fatalf("expected error in marshaled output, got %#v", payload)
	}
}

func TestHostBashReadsRunEnvironmentSnapshotWithoutChangingProcessEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertion uses POSIX quoting")
	}
	root := t.TempDir()
	scope := runenv.NewScope(runenv.Limits{})
	defer scope.Destroy()
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "RUN_LOCAL_VALUE", Value: "dynamic-value"}); err != nil {
		t.Fatal(err)
	}
	before, presentBefore := os.LookupEnv("RUN_LOCAL_VALUE")
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"bash"}, ShellFeaturesEnabled: true, ShellExecutable: "bash", MaxCommandChars: 16000}}}
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": `printf '%s' "$RUN_LOCAL_VALUE"`}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root}, StaticRuntimeEnv: map[string]string{"RUN_LOCAL_VALUE": "static-value"}, RunEnvironment: scope,
	})
	if err != nil || result.Error != "" || result.Output != "dynamic-value" {
		t.Fatalf("bash result=%#v err=%v", result, err)
	}
	after, presentAfter := os.LookupEnv("RUN_LOCAL_VALUE")
	if before != after || presentBefore != presentAfter {
		t.Fatalf("process environment changed: before=(%q,%v) after=(%q,%v)", before, presentBefore, after, presentAfter)
	}
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationUnset, Name: "RUN_LOCAL_VALUE"}); err != nil {
		t.Fatal(err)
	}
	result, err = executor.invokeHostBash(context.Background(), map[string]any{"command": `printf '%s' "$RUN_LOCAL_VALUE"`}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root}, StaticRuntimeEnv: map[string]string{"RUN_LOCAL_VALUE": "static-value"}, RunEnvironment: scope,
	})
	if err != nil || result.Error != "" || result.Output != "static-value" {
		t.Fatalf("bash fallback result=%#v err=%v", result, err)
	}
	scope.Destroy()
	if _, err := mergeCommandEnv(&contracts.ExecutionContext{RunEnvironment: scope}); !errors.Is(err, runenv.ErrClosed) {
		t.Fatalf("closed run environment snapshot error = %v, want ErrClosed", err)
	}
}

func TestMergeEnvironmentListUsesCaseInsensitiveReplacement(t *testing.T) {
	merged := mergeEnvironmentList([]string{"Path=base", "OTHER=value"}, map[string]string{"PATH": "override"})
	count := 0
	for _, item := range merged {
		name, value, _ := strings.Cut(item, "=")
		if strings.EqualFold(name, "PATH") {
			count++
			if value != "override" {
				t.Fatalf("PATH value = %q", value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("case-insensitive PATH count = %d: %#v", count, merged)
	}
}

func bashExecutionContext(workspaceRoot string) *contracts.ExecutionContext {
	return &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			WorkspaceRoot: workspaceRoot,
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{WorkspaceDir: workspaceRoot},
			},
		},
	}
}

func mustMergeCommandEnv(t *testing.T, execCtx *contracts.ExecutionContext) []string {
	t.Helper()
	env, err := mergeCommandEnv(execCtx)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func mustMergeBashCommandEnv(t *testing.T, execCtx *contracts.ExecutionContext, identityFile string) []string {
	t.Helper()
	env, err := mergeBashCommandEnv(execCtx, identityFile)
	if err != nil {
		t.Fatal(err)
	}
	return env
}
