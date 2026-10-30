package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/builtins"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hostenv"
	"agent-platform/internal/hostshell"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/shellenv"
	shellsyntax "agent-platform/internal/shellreview/syntax"
	"agent-platform/internal/textcodec"
)

const defaultBashTimeoutSeconds int64 = 60

func (t *RuntimeToolExecutor) invokeHostBash(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if reason := shellsyntax.CheckText(stringArg(args, "command")); reason != "" {
		return ToolExecutionResult{Output: reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	if len(command) > maxInt(t.cfg.Bash.MaxCommandChars, 16000) {
		return ToolExecutionResult{Output: "Command is too long", Error: "command_too_long", ExitCode: -1}, nil
	}
	var err error
	execCtx, err = hostshell.FreezeContext(execCtx)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "run_env_snapshot_failed", ExitCode: -1}, nil
	}
	session := t.policySession(execCtx)
	rawCwd := strings.TrimSpace(stringArg(args, "cwd"))
	if rawCwd == "" {
		if strings.TrimSpace(accesspolicy.SessionWorkspaceRoot(session)) == "" && accesspolicy.DefaultBashCwd(session) == "@workspace" {
			return ToolExecutionResult{Output: "workspace_unavailable: no Workspace; pass cwd explicitly, usually @chat", Error: "workspace_unavailable", ExitCode: -1}, nil
		}
		rawCwd = accesspolicy.DefaultBashCwd(session)
	}
	workingDir, err := accesspolicy.ResolveSessionPath(session, rawCwd)
	if err != nil {
		code := "bash_invalid_cwd"
		if strings.Contains(err.Error(), "workspace_unavailable") {
			code = "workspace_unavailable"
		}
		return ToolExecutionResult{Output: err.Error(), Error: code, ExitCode: -1}, nil
	}
	commandEnv, err := t.commandEnv(execCtx)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "run_env_snapshot_failed", ExitCode: -1}, nil
	}
	runtimeInfo := t.runtimeInfo()
	launchOptions := hostshell.Options{GOOS: runtimeInfo.GOOS, Command: command, CWD: workingDir, Env: commandEnv}
	direct := accesspolicy.DirectConnectorInvocation(session, command, workingDir, bashEnvironmentVariables(commandEnv))
	var launch hostshell.LaunchSpec
	reviewEnv := &accesspolicy.BashEnvironment{}
	if direct == nil {
		launch, err = hostshell.ResolveTool(t.cfg.Bash, launchOptions)
		if err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "bash_launch_unavailable", ExitCode: -1}, nil
		}
		reviewEnv.Launch = &launch.Binding
	}
	rawAccessReview := accesspolicy.ReviewBashCommandInEnvironment(t.cfg.AccessPolicy, session, command, workingDir, bashEnvironmentVariables(commandEnv), reviewEnv, execCtx)
	securityReview := rawAccessReview.SecurityReview(command, bashSecurityKnownVariables(execCtx))
	switch securityReview.Decision {
	case bashsec.ReviewAllow:
	case bashsec.ReviewRequiresApproval:
	default:
		return ToolExecutionResult{Output: securityReview.Reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	if len(t.cfg.Bash.AllowedCommands) == 0 && !rawAccessReview.ConnectorOnly {
		return ToolExecutionResult{Output: "Bash command whitelist is empty", Error: "command_whitelist_empty", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled && rawAccessReview.UnknownDialect {
		return ToolExecutionResult{Output: "Strict command validation requires a supported shell grammar", Error: "command_not_allowed", ExitCode: -1}, nil
	}
	accessReview := accesspolicy.PendingBashPlan(execCtx, rawAccessReview)
	switch accessReview.Decision {
	case accesspolicy.DecisionAllow, accesspolicy.DecisionAutoApproved:
	case accesspolicy.DecisionRequiresApproval:
		if !accesspolicy.HasApproval(execCtx, accessReview) {
			return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_approval_required", ExitCode: -1}, nil
		}
	default:
		return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_blocked", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled {
		strictConfig := t.cfg.Bash
		if rawAccessReview.HasConnector {
			strictConfig.AllowedCommands = append(append([]string(nil), strictConfig.AllowedCommands...), ":")
		}
		if err := validateStrictCommand(rawAccessReview.ShellReviewCommand(command), strictConfig); err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "command_not_allowed", ExitCode: -1}, nil
		}
	}

	timeoutSeconds := t.resolveBashTimeoutSeconds(args, execCtx)
	timeout := time.Duration(timeoutSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, launch.Executable, launch.Args...)
	cmd.WaitDelay = bashOutputPipeWaitDelay
	cmd.Dir = launch.CWD
	// Only credentials require a verified direct child; public cli.json env is
	// already in every Host Bash command, so composite commands keep working.
	if direct == nil && execCtx != nil && executesCredentialedConnector(session, rawAccessReview.ConnectorIDs) {
		return ToolExecutionResult{Output: "Credentialed connector commands must be invoked as a single direct command; split shell pipelines, wrappers and redirects into separate tool calls.", Error: "connector_requires_direct_invocation", ExitCode: -1}, nil
	}
	cmd.Env = launch.Env
	if direct != nil {
		childEnv, envErr := t.connectorCommandEnvironment(runCtx, execCtx, direct.ID, commandEnv)
		if envErr != nil {
			return ToolExecutionResult{Output: envErr.Error(), Error: "connector_credentials_unavailable", ExitCode: -1}, nil
		}
		cmd, err = directConnectorCommand(runCtx, direct.Program, direct.Args, workingDir, childEnv, runtime.GOOS)
		if err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "connector_execution_unavailable", ExitCode: -1}, nil
		}
		cmd.WaitDelay = bashOutputPipeWaitDelay
	}

	stdoutFile, err := os.CreateTemp("", "agent-platform-bash-stdout-*.log")
	if err != nil {
		return bashResult("", err.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	defer cleanupBashOutputFile(stdoutFile)
	stderrFile, err := os.CreateTemp("", "agent-platform-bash-stderr-*.log")
	if err != nil {
		return bashResult("", err.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	defer cleanupBashOutputFile(stderrFile)
	var outputSink ToolOutputSink
	if execCtx != nil {
		outputSink = execCtx.ToolOutputSink
	}
	stdoutCapture := newBashOutputCapture(ctx, stdoutFile, outputSink, ToolOutputStdout)
	stderrCapture := newBashOutputCapture(ctx, stderrFile, outputSink, ToolOutputStderr)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture

	// Revalidate immediately before launch against the environment actually passed
	// to the child. Only this final check consumes a one-shot approval.
	if direct == nil {
		freshLaunch, err := hostshell.ResolveTool(t.cfg.Bash, launchOptions)
		if err != nil || freshLaunch.Binding.Fingerprint != launch.Binding.Fingerprint {
			return ToolExecutionResult{Output: "Shell launch changed before execution; retry for a new review", Error: "bash_launch_changed", ExitCode: -1}, nil
		}
		reviewEnv.Launch = &freshLaunch.Binding
	}
	// Freeze process inputs, not the live permission level. A downgrade while
	// queued or preparing credentials must still affect the final review.
	session = t.policySession(execCtx)
	rawFinalReview := accesspolicy.ReviewBashCommandInEnvironment(t.cfg.AccessPolicy, session, command, workingDir, bashEnvironmentVariables(commandEnv), reviewEnv, execCtx)
	securityReview = rawFinalReview.SecurityReview(command, bashEnvironmentVariables(commandEnv))
	if direct != nil {
		fresh := accesspolicy.DirectConnectorInvocation(session, command, workingDir, bashEnvironmentVariables(commandEnv))
		if fresh == nil || fresh.ID != direct.ID || fresh.Program != direct.Program {
			return ToolExecutionResult{Output: "Connector entry changed before launch", Error: "connector_entry_changed", ExitCode: -1}, nil
		}
	}
	if securityReview.Decision == bashsec.ReviewBlock {
		return ToolExecutionResult{Output: securityReview.Reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	approvalSource := accesspolicy.BashApprovalSource(execCtx, rawFinalReview)
	accessReview = accesspolicy.PendingBashPlan(execCtx, rawFinalReview)
	if accessReview.Blocked() {
		return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_blocked", ExitCode: -1}, nil
	}
	if accessReview.RequiresApproval() && !accesspolicy.ConsumeApproval(execCtx, accessReview) {
		return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_approval_required", ExitCode: -1}, nil
	}
	if securityReview.Decision == bashsec.ReviewRequiresApproval && !securityReview.AutoApprovedAtLevel(session.AccessLevel) && !consumeBashSecurityApproval(execCtx, securityReview.Fingerprint) {
		return ToolExecutionResult{Output: securityReview.Reason, Error: "bash_security_approval_required", ExitCode: -1}, nil
	}
	if rawFinalReview.UsesSSHAgent && direct == nil {
		// The SSH agent is a credential: only reviewed Git network operations get it.
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			cmd.Env = append(cmd.Env, "SSH_AUTH_SOCK="+sock)
		}
	}
	err = cmd.Run()
	stdoutCapture.Close()
	stderrCapture.Close()
	if captureErr := firstBashOutputCaptureError(stdoutCapture, stderrCapture); captureErr != nil {
		return bashResult("", captureErr.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	err = normalizeBashCommandError(err, runCtx, cmd)
	stdout, readErr := readBashOutputFile(stdoutFile, runtimeInfo)
	if readErr != nil {
		return bashResult("", readErr.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	stderr, readErr := readBashOutputFile(stderrFile, runtimeInfo)
	if readErr != nil {
		return bashResult("", readErr.Error(), "host", workingDir, -1, "bash_output_capture_failed"), nil
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if strings.TrimSpace(stderr) == "" {
			stderr = err.Error()
		}
		if runCtx.Err() == context.DeadlineExceeded && strings.TrimSpace(stderr) == "" {
			stderr = "Command timed out"
		}
	}
	result := bashResult(stdout, stderr, "host", workingDir, exitCode, "")
	if accessReview.AutoApproved() {
		appendBashAccessPolicyMetadata(&result, accessReview, stdout, stderr, workingDir, exitCode)
	}
	if approvalSource != "" || accessReview.HasConnector || accessReview.RuleKey == "bash-access:authored-script" || accessReview.RuleKey == "bash-access:temp-script" || accessReview.RuleKey == "bash-access:skill-script" {
		if result.Structured == nil {
			result.Structured = map[string]any{"stdout": stdout, "stderr": stderr, "mode": "host", "cwd": workingDir, "exitCode": exitCode}
		}
		result.Structured["accessPolicy"] = accesspolicy.BashPlanMetadata(accessReview)
		if approvalSource != "" {
			metadata := result.Structured["accessPolicy"].(map[string]any)
			metadata["decision"] = "allow"
			metadata["approvalSource"] = approvalSource
		}
	}
	return result, nil
}

func firstBashOutputCaptureError(captures ...*bashOutputCapture) error {
	for _, capture := range captures {
		if err := capture.Err(); err != nil {
			return err
		}
	}
	return nil
}

// executesCredentialedConnector reports whether the command runs a connector
// that receives credentials; another connector's credentials do not count.
func executesCredentialedConnector(session QuerySession, ids []string) bool {
	for _, binding := range session.ConnectorCredentials {
		for _, id := range ids {
			if binding.ID == id {
				return true
			}
		}
	}
	return false
}

// directConnectorCommand starts a verified connector entry. Only Windows needs
// the shared adapter: npm cmd shims become Node + script argv, so payloads never
// pass through cmd.exe. Elsewhere the entry still runs itself, honoring its own
// shebang and the Bash tool's process handling.
func directConnectorCommand(ctx context.Context, program string, args []string, dir string, env []string, goos string) (*exec.Cmd, error) {
	if goos == "windows" {
		return connectorauth.ExecutionProgramCommand(ctx, program, args, dir, env)
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env = dir, env
	return cmd, nil
}

func normalizeBashCommandError(err error, runCtx context.Context, cmd *exec.Cmd) error {
	if !errors.Is(err, exec.ErrWaitDelay) || runCtx == nil || runCtx.Err() != nil || cmd == nil || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		return err
	}
	return nil
}

func (t *RuntimeToolExecutor) resolveBashTimeoutSeconds(args map[string]any, execCtx *ExecutionContext) int64 {
	requested := int64Arg(args, "timeout")
	budgetTimeout := int64(0)
	if execCtx != nil {
		budget := NormalizeBudget(execCtx.Budget)
		budgetTimeout = int64(budget.Tool.Timeout)
	}
	if requested <= 0 {
		if budgetTimeout > 0 {
			return budgetTimeout
		}
		return defaultBashTimeoutSeconds
	}
	if budgetTimeout > 0 && requested > budgetTimeout {
		return budgetTimeout
	}
	return requested
}

func cleanupBashOutputFile(file *os.File) {
	if file == nil {
		return
	}
	_ = file.Close()
	_ = os.Remove(file.Name())
}

func readBashOutputFile(file *os.File, runtimeInfo runtimeenv.Info) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}
	output, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return textcodec.DecodeSubprocessOutput(output, runtimeInfo), nil
}

func appendBashAccessPolicyMetadata(result *ToolExecutionResult, review accesspolicy.BashPlan, stdout, stderr, cwd string, exitCode int) {
	if result == nil || !review.AutoApproved() {
		return
	}
	if result.Structured == nil {
		result.Structured = map[string]any{
			"exitCode": exitCode,
			"mode":     "host",
			"cwd":      cwd,
			"stdout":   stdout,
			"stderr":   stderr,
		}
	}
	result.Structured["accessPolicy"] = accesspolicy.BashPlanMetadata(review)
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
	if execCtx == nil {
		return nil
	}
	runtimeEnv := execCtx.StaticRuntimeEnv
	if execCtx.RunEnvironment != nil {
		if dynamic, _, err := execCtx.RunEnvironment.Snapshot(); err == nil {
			runtimeEnv = agentconfig.Merge(runtimeEnv, dynamic)
		}
	}
	return agentconfig.Merge(
		runtimeEnv,
		agentconfig.HostEnvironment(
			execCtx.Session.RuntimeContext.LocalPaths.AgentDir,
			execCtx.Session.RuntimeContext.LocalPaths.WorkspaceDir,
			execCtx.Session.RuntimeContext.LocalPaths.ChatDir,
		),
	)
}

var unsupportedBashCommands = map[string]bool{
	".": true, "source": true, "eval": true, "exec": true,
	"coproc": true, "fg": true, "bg": true, "jobs": true,
}

func validateStrictCommand(command string, cfg config.BashConfig) error {
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

func connectorBins(execCtx *ExecutionContext) []string {
	if execCtx == nil {
		return nil
	}
	return append(append([]string(nil), execCtx.Session.ConnectorBinDirs...), execCtx.Session.ConnectorInstallBinDirs...)
}

// commandEnv is the single environment used for both access review and launch.
func (t *RuntimeToolExecutor) commandEnv(execCtx *ExecutionContext) ([]string, error) {
	return mergeCommandEnvWith(execCtx, t.cfg.Bash.InheritEnv)
}

func mergeCommandEnvWith(execCtx *ExecutionContext, inherit []string) ([]string, error) {
	env := hostenv.WithSystemPaths(shellenv.InheritedEnvironment(os.Environ(), inherit...))
	var agentDir string
	var workspaceDir string
	var chatDir string
	var runtimeEnv map[string]string
	if execCtx != nil {
		agentDir = execCtx.Session.RuntimeContext.LocalPaths.AgentDir
		workspaceDir = execCtx.Session.RuntimeContext.LocalPaths.WorkspaceDir
		chatDir = execCtx.Session.RuntimeContext.LocalPaths.ChatDir
		runtimeEnv = execCtx.StaticRuntimeEnv
	}
	if execCtx != nil && execCtx.RunEnvironment != nil {
		dynamic, _, err := execCtx.RunEnvironment.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot run environment: %w", err)
		}
		runtimeEnv = agentconfig.Merge(runtimeEnv, dynamic)
	}
	// Runtime maps come from validated definitions, but strip reserved names
	// again at execution to protect restored or internal call contexts.
	cleanRuntime := map[string]string{}
	for key, value := range runtimeEnv {
		if !agentconfig.IsReserved(key) {
			cleanRuntime[key] = value
		}
	}
	var connectorEnv map[string]string
	if execCtx != nil {
		connectorEnv = execCtx.Session.ConnectorEnv
	}
	// Public cli.json env of mounted connectors, as in the Workspace Terminal.
	overrides := agentconfig.Merge(
		cleanRuntime,
		connectorEnv,
		agentconfig.HostEnvironment(agentDir, workspaceDir, chatDir),
	)
	if len(overrides) > 0 {
		env = mergeEnvironmentList(env, overrides)
	}
	env = connector.WithPath(builtins.EnsureBinInEnv(env), connectorBins(execCtx))
	if execCtx != nil {
		// Skill PATH entries extend the search path; they never shadow system tools.
		env = hostenv.AppendPath(env, execCtx.Session.PathAppend...)
	}
	return agentconfig.WithIdentityEnvironment(env, nil), nil
}

func mergeEnvironmentList(base []string, overrides map[string]string) []string {
	result := append([]string(nil), base...)
	indexes := map[string]int{}
	for index, item := range result {
		name, _, ok := strings.Cut(item, "=")
		if ok {
			indexes[strings.ToUpper(name)] = index
		}
	}
	for key, value := range overrides {
		lookup := strings.ToUpper(key)
		if index, ok := indexes[lookup]; ok {
			result[index] = key + "=" + value
			continue
		}
		indexes[lookup] = len(result)
		result = append(result, key+"="+value)
	}
	return result
}

func (t *RuntimeToolExecutor) connectorCommandEnvironment(ctx context.Context, execCtx *ExecutionContext, id string, base []string) ([]string, error) {
	// base already carries every mounted connector's public env.
	env := append([]string(nil), base...)
	if execCtx == nil {
		return env, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, binding := range execCtx.Session.ConnectorCredentials {
		if binding.ID != id {
			continue
		}
		values, err := connectorauth.ResolveEnvironment(ctx, binding, t.cfg.SsoIdTokenFile)
		if err != nil {
			return nil, err
		}
		env = mergeEnvironmentList(env, values)
	}
	return env, nil
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
