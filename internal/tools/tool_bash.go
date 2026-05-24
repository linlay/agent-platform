package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func (t *RuntimeToolExecutor) invokeHostBash(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	if len(command) > maxInt(t.cfg.Bash.MaxCommandChars, 16000) {
		return ToolExecutionResult{Output: "Command is too long", Error: "command_too_long", ExitCode: -1}, nil
	}
	securityReview := bashsec.ReviewBashSecurityWithKnownVariables(command, bashSecurityKnownVariables(execCtx))
	switch securityReview.Decision {
	case bashsec.ReviewAllow:
	case bashsec.ReviewRequiresApproval:
		if !consumeBashSecurityApproval(execCtx, securityReview.Fingerprint) {
			return ToolExecutionResult{Output: securityReview.Reason, Error: "bash_security_approval_required", ExitCode: -1}, nil
		}
	default:
		return ToolExecutionResult{Output: securityReview.Reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	if len(t.cfg.Bash.AllowedCommands) == 0 {
		return ToolExecutionResult{Output: "Bash command whitelist is empty", Error: "command_whitelist_empty", ExitCode: -1}, nil
	}
	workingDir := defaultStringArg(args, "cwd", t.cfg.Bash.WorkingDirectory)
	if execCtx != nil && strings.TrimSpace(stringArg(args, "cwd")) == "" {
		if workspaceRoot := filetools.SessionWorkspaceRoot(execCtx.Session); workspaceRoot != "" {
			workingDir = workspaceRoot
		}
	}
	if workingDir == "" {
		workingDir = "."
	}
	accessReview := accesspolicy.ReviewBashCommand(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, workingDir), command, workingDir, bashSecurityKnownVariables(execCtx))
	switch accessReview.Decision {
	case accesspolicy.DecisionAllow, accesspolicy.DecisionAutoApproved:
	case accesspolicy.DecisionRequiresApproval:
		if !accesspolicy.ConsumeApproval(execCtx, accessReview) {
			return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_approval_required", ExitCode: -1}, nil
		}
	default:
		return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_blocked", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled {
		if err := validateStrictCommand(command, t.cfg.Bash, workingDir); err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "command_not_allowed", ExitCode: -1}, nil
		}
	}

	timeoutMs := int64Arg(args, "timeout_ms")
	if timeoutMs <= 0 {
		timeoutMs = int64(maxInt(t.cfg.Bash.ShellTimeoutMs, 30000))
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellExecutable, shellArgs := resolveHostShellInvocation(t.cfg.Bash, command, runtime.GOOS)
	cmd := exec.CommandContext(runCtx, shellExecutable, shellArgs...)
	cmd.Dir = workingDir
	cmd.Env = mergeCommandEnv(execCtx)

	outputFile, err := os.CreateTemp("", "agent-platform-bash-*.log")
	if err != nil {
		return bashResult("", err.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	defer func() {
		_ = outputFile.Close()
		_ = os.Remove(outputFile.Name())
	}()
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile

	err = cmd.Run()
	if _, seekErr := outputFile.Seek(0, 0); seekErr != nil {
		return bashResult("", seekErr.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	output, readErr := io.ReadAll(outputFile)
	if readErr != nil {
		return bashResult("", readErr.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	exitCode := 0
	stderr := ""
	stdout := string(output)
	if len(stdout) > maxBashOutputChars {
		stdout = stdout[:maxBashOutputChars]
	}
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		stderr = err.Error()
		if runCtx.Err() == context.DeadlineExceeded {
			stderr = "Command timed out"
		}
	}
	result := bashResult(stdout, stderr, "host", workingDir, exitCode, "")
	if accessReview.AutoApproved() {
		appendBashAccessPolicyMetadata(&result, accessReview, stdout, stderr, workingDir, exitCode)
	}
	return result, nil
}

func appendBashAccessPolicyMetadata(result *ToolExecutionResult, review accesspolicy.BashPlan, stdout, stderr, workingDir string, exitCode int) {
	if result == nil || !review.AutoApproved() {
		return
	}
	if result.Structured == nil {
		result.Structured = map[string]any{
			"exitCode":         exitCode,
			"mode":             "host",
			"workingDirectory": workingDir,
			"stdout":           stdout,
			"stderr":           stderr,
		}
	}
	result.Structured["accessPolicy"] = map[string]any{
		"accessLevel": review.AccessLevel,
		"decision":    "auto_approved",
		"ruleKey":     review.RuleKey,
	}
}

const hostShellCommandPlaceholder = "{{command}}"

func resolveHostShellInvocation(cfg config.BashConfig, command string, goos string) (string, []string) {
	shellExecutable := strings.TrimSpace(cfg.ShellExecutable)
	shellArgs := compactShellArgs(cfg.ShellArgs)

	if shellExecutable == "" {
		shellExecutable = defaultHostShellExecutable(goos)
	}
	if len(shellArgs) == 0 {
		shellArgs = defaultHostShellArgs(shellExecutable, goos)
	}
	return shellExecutable, expandHostShellArgs(shellArgs, command)
}

func defaultHostShellExecutable(goos string) string {
	if goos == "windows" {
		return "powershell.exe"
	}
	return "bash"
}

func defaultHostShellArgs(shellExecutable string, goos string) []string {
	base := normalizedShellBase(shellExecutable)
	if goos == "windows" {
		switch base {
		case "", "powershell", "pwsh":
			return []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", hostShellCommandPlaceholder}
		case "cmd":
			return []string{"/d", "/s", "/c", hostShellCommandPlaceholder}
		case "bash", "sh":
			return []string{"-lc", hostShellCommandPlaceholder}
		default:
			return []string{hostShellCommandPlaceholder}
		}
	}
	return []string{"-lc", hostShellCommandPlaceholder}
}

func normalizedShellBase(shellExecutable string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(shellExecutable)))
	return strings.TrimSuffix(base, ".exe")
}

func compactShellArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func expandHostShellArgs(args []string, command string) []string {
	out := make([]string, 0, len(args)+1)
	replaced := false
	for _, arg := range args {
		if strings.Contains(arg, hostShellCommandPlaceholder) {
			out = append(out, strings.ReplaceAll(arg, hostShellCommandPlaceholder, command))
			replaced = true
			continue
		}
		out = append(out, arg)
	}
	if !replaced {
		out = append(out, command)
	}
	return out
}

func consumeBashSecurityApproval(execCtx *ExecutionContext, fingerprint string) bool {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" || len(execCtx.BashSecurityApprovals) == 0 {
		return false
	}
	remaining := execCtx.BashSecurityApprovals[fingerprint]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(execCtx.BashSecurityApprovals, fingerprint)
		return true
	}
	execCtx.BashSecurityApprovals[fingerprint] = remaining - 1
	return true
}

func bashSecurityKnownVariables(execCtx *ExecutionContext) map[string]string {
	if execCtx == nil || len(execCtx.RuntimeEnvOverrides) == 0 {
		return nil
	}
	return execCtx.RuntimeEnvOverrides
}

var unsupportedBashCommands = map[string]bool{
	".": true, "source": true, "eval": true, "exec": true,
	"coproc": true, "fg": true, "bg": true, "jobs": true,
}

const maxBashOutputChars = 8000

func validateStrictCommand(command string, cfg config.BashConfig, workingDirectory string) error {
	if strings.ContainsAny(command, "\n;&|<>(){}") {
		return fmt.Errorf("Unsupported syntax for bash")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("Cannot parse command")
	}
	base := fields[0]
	if unsupportedBashCommands[strings.ToLower(base)] {
		return fmt.Errorf("Unsupported command: %s", base)
	}
	if !containsString(cfg.AllowedCommands, base) {
		return fmt.Errorf("Command not allowed: %s", base)
	}
	return nil
}

func stringMapArg(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case map[string]string:
		return CloneStringMap(value)
	case map[string]any:
		result := make(map[string]string, len(value))
		for envKey, envValue := range value {
			text, ok := envValue.(string)
			if !ok {
				continue
			}
			result[envKey] = text
		}
		if len(result) == 0 {
			return nil
		}
		return result
	default:
		return nil
	}
}

func mergeCommandEnv(execCtx *ExecutionContext) []string {
	env := append([]string(nil), os.Environ()...)
	if execCtx == nil || len(execCtx.RuntimeEnvOverrides) == 0 {
		return env
	}
	for key, value := range execCtx.RuntimeEnvOverrides {
		found := false
		prefix := key + "="
		for idx, item := range env {
			if strings.HasPrefix(item, prefix) {
				env[idx] = prefix + value
				found = true
				break
			}
		}
		if !found {
			env = append(env, prefix+value)
		}
	}
	return env
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "*" {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func int64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	default:
		return 0
	}
}
