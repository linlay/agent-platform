package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

const sandboxShellExecutable = "/bin/sh"
const sandboxShellStartup = "-lc"

// ReviewShellLaunch describes the actual transport without opening a session
// or running guest code. A login sh and its startup files are not a verified
// Bash profile. These hashes bind requested inputs, not unobserved guest bytes.
func (s *ContainerHubSandboxService) ReviewShellLaunch(execCtx *contracts.ExecutionContext, command, cwd string, invocationEnv map[string]string) (types.LaunchBinding, error) {
	if execCtx == nil {
		return types.LaunchBinding{}, fmt.Errorf("missing execution context")
	}
	if !s.cfg.Enabled {
		return types.LaunchBinding{}, fmt.Errorf("container-hub sandbox is disabled")
	}
	if err := agentconfig.ValidateUserEnvironment(invocationEnv); err != nil {
		return types.LaunchBinding{}, err
	}
	env, err := sandboxCommandEnvironment(execCtx, invocationEnv)
	if err != nil {
		return types.LaunchBinding{}, err
	}
	environmentID, level := s.resolveEnvironmentID(execCtx), s.resolveRuntimeLevel(execCtx)
	sessionID := ""
	if execCtx.SandboxSession != nil {
		sessionID = execCtx.SandboxSession.SessionID
		if execCtx.SandboxSession.EnvironmentID != "" {
			environmentID = execCtx.SandboxSession.EnvironmentID
		}
		level = execCtx.SandboxSession.Level
	}
	if environmentID == "" {
		return types.LaunchBinding{}, fmt.Errorf("container-hub environment id is required")
	}
	template := []string{sandboxShellStartup, "{{command}}"}
	rule := shellContractHash([]string{s.cfg.BaseURL, sandboxShellExecutable, sandboxShellStartup, "unattested-login-shell"})
	contract := shellContractHash(struct {
		Rule, Environment, Level, Session, Run, Agent, CWD, Access string
		Env                                                        map[string]string
	}{rule, environmentID, level, sessionID, execCtx.Session.RunID, execCtx.Session.AgentKey, strings.TrimSpace(cwd), execCtx.Session.AccessLevel, env})
	return types.LaunchBinding{Executable: sandboxShellExecutable, Template: template, Dialect: dialect.Unknown, SyntaxSupported: false, Reason: "Container Hub runs /bin/sh -lc; guest shell bytes and login startup files are not attested", RuleFingerprint: rule, ContractFingerprint: contract, Fingerprint: shellContractHash([]string{contract, command})}, nil
}
func shellContractHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
