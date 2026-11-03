package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

// Server protocol fixtures emulate a verified Bash backend. They do not stand
// in for the actual Container Hub login-sh transport or its missing evidence.
func serverFixtureBashLaunch(ctx *contracts.ExecutionContext, command, cwd string, env map[string]string) (types.LaunchBinding, error) {
	hash := func(value any) string {
		raw, _ := json.Marshal(value)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	rule := hash("server-test-verified-bash")
	contract := hash(struct {
		Rule, CWD          string
		Static, Invocation map[string]string
	}{rule, cwd, ctx.StaticRuntimeEnv, env})
	return types.LaunchBinding{Executable: "/verified-fixture/bash", Template: []string{"--noprofile", "--norc", "-c", "{{command}}"}, Dialect: dialect.Bash, SyntaxSupported: true, Reason: "explicit server protocol fixture", RuleFingerprint: rule, ContractFingerprint: contract, Fingerprint: hash([]string{contract, command})}, nil
}
func (*recordingSandbox) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return serverFixtureBashLaunch(c, s, d, e)
}
func (*scriptedSandbox) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return serverFixtureBashLaunch(c, s, d, e)
}
