package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

// Missing launch evidence is never permission to assume Bash or run a guest
// resolver through a potentially different shell and its initialization files.
func unverifiedSandboxLaunch(command, cwd string, env map[string]string) types.LaunchBinding {
	hash := func(value any) string {
		data, _ := json.Marshal(value)
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	rule := hash("guest-launch-contract-unavailable")
	contract := hash(struct {
		Rule, CWD string
		Env       map[string]string
	}{rule, cwd, env})
	return types.LaunchBinding{Executable: "unverified guest shell", Dialect: dialect.Unknown, SyntaxSupported: false, Reason: "guest shell launch contract is unavailable", RuleFingerprint: rule, ContractFingerprint: contract, Fingerprint: hash([]string{contract, command})}
}
