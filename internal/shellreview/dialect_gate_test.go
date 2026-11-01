package shellreview

import (
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

func TestNonBashSupportedFlagCannotBorrowBashParserOrStartProbes(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAutoApprove)
	f.session.AgentHasRuntimeSandbox = true
	for _, grammar := range []dialect.Dialect{dialect.PowerShell, dialect.CMD, dialect.MSYS, dialect.Unknown} {
		env := &types.BashEnvironment{Launch: &types.LaunchBinding{Executable: "guest", Dialect: grammar, SyntaxSupported: true, Fingerprint: "call", ContractFingerprint: "contract", RuleFingerprint: "rule"}, Directory: func(string) (string, error) { t.Fatal("unproven grammar started a guest probe"); return "", nil }}
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, "echo safe", f.workspace, nil, env, nil)
		if !p.UnknownDialect || !p.RequiresApproval() || p.Launch == nil || p.Launch.SyntaxSupported {
			t.Fatalf("%s borrowed Bash semantics: %+v", grammar, p)
		}
		if !env.Launch.SyntaxSupported {
			t.Fatal("review mutated the caller's launch evidence")
		}
	}
}
