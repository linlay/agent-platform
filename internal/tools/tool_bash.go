package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/bashsec"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeHostBash(ctx context.Context, args map[string]any) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	if len(command) > maxInt(t.cfg.Bash.MaxCommandChars, 16000) {
		return ToolExecutionResult{Output: "Command is too long", Error: "command_too_long", ExitCode: -1}, nil
	}
	if ok, reason := bashsec.CheckBashSecurity(command); !ok {
		return ToolExecutionResult{Output: reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	if len(t.cfg.Bash.AllowedCommands) == 0 {
		return ToolExecutionResult{Output: "Bash command whitelist is empty", Error: "command_whitelist_empty", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled {
		if err := validateStrictCommand(command, t.cfg.Bash); err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "command_not_allowed", ExitCode: -1}, nil
		}
	}

	timeout := time.Duration(maxInt(t.cfg.Bash.ShellTimeoutMs, 30000)) * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellExecutable := strings.TrimSpace(t.cfg.Bash.ShellExecutable)
	if shellExecutable == "" {
		shellExecutable = "bash"
	}
	cmd := exec.CommandContext(runCtx, shellExecutable, "-lc", command)
	workingDir := t.cfg.Bash.WorkingDirectory
	if workingDir == "" {
		workingDir = "."
	}
	cmd.Dir = workingDir
	output, err := cmd.CombinedOutput()
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
	payload := map[string]any{
		"exitCode":         exitCode,
		"mode":             "host",
		"workingDirectory": workingDir,
		"stdout":           stdout,
		"stderr":           stderr,
	}
	return structuredResultWithExit(payload, exitCode), nil
}

var unsupportedBashCommands = map[string]bool{
	".": true, "source": true, "eval": true, "exec": true,
	"coproc": true, "fg": true, "bg": true, "jobs": true,
}

const maxBashOutputChars = 8000

func validateStrictCommand(command string, cfg config.BashConfig) error {
	if strings.ContainsAny(command, "\n;&|<>(){}") {
		return fmt.Errorf("Unsupported syntax for _bash_")
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
	if !containsString(cfg.PathCheckedCommands, base) || containsString(cfg.PathCheckBypassCommands, base) {
		return nil
	}
	workingDirectory := cfg.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		if !looksLikePathArg(field) {
			continue
		}
		resolved := field
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workingDirectory, resolved)
		}
		resolved = filepath.Clean(resolved)
		if !pathAllowed(resolved, cfg.AllowedPaths, workingDirectory) {
			return fmt.Errorf("Path not allowed: %s", field)
		}
	}
	return nil
}

func looksLikePathArg(arg string) bool {
	return strings.Contains(arg, "/") || strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "~")
}

func pathAllowed(resolved string, allowed []string, workingDirectory string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, root := range allowed {
		if root == "" {
			continue
		}
		checkRoot := root
		if !filepath.IsAbs(checkRoot) {
			checkRoot = filepath.Join(workingDirectory, checkRoot)
		}
		checkRoot = filepath.Clean(checkRoot)
		if resolved == checkRoot || strings.HasPrefix(resolved, checkRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
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
