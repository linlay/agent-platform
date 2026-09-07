package hostshell

import (
	"fmt"
	"os"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/temppaths"
)

// FreezeContext takes one run.env snapshot for review AND process creation.
// Approval maps remain shared, but the command cannot take a second snapshot
// with different variable values after its paths have been authorized.
func FreezeContext(original *contracts.ExecutionContext) (*contracts.ExecutionContext, error) {
	if original == nil {
		return nil, nil
	}
	frozen := *original
	if original.RunEnvironment != nil {
		dynamic, _, err := original.RunEnvironment.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot run environment: %w", err)
		}
		frozen.StaticRuntimeEnv = agentconfig.Merge(original.StaticRuntimeEnv, dynamic)
		frozen.RunEnvironment = nil
	}
	return &frozen, nil
}

func TempDir(execCtx *contracts.ExecutionContext) string {
	if execCtx != nil && execCtx.Session.TempRoot != "" {
		return execCtx.Session.TempRoot
	}
	if primary, ok := temppaths.System().Primary(); ok {
		return primary.Host
	}
	return ""
}

// KnownVariables intentionally excludes inherited credentials and the identity
// access token. These values may appear in access-policy diagnostics.
func KnownVariables(frozen *contracts.ExecutionContext, cfg config.BashConfig, goos string) map[string]string {
	var values map[string]string
	if frozen != nil {
		paths := frozen.Session.RuntimeContext.LocalPaths
		values = agentconfig.Merge(frozen.StaticRuntimeEnv, agentconfig.HostEnvironment(paths.AgentDir, paths.WorkspaceDir, paths.ChatDir))
	}
	if Enabled(cfg, goos) {
		if values == nil {
			values = map[string]string{}
		}
		home, _ := os.UserHomeDir()
		values["HOME"] = home
		values["TMPDIR"], values["TMP"], values["TEMP"] = TempDir(frozen), TempDir(frozen), TempDir(frozen)
	}
	delete(values, agentconfig.EnvAccessToken)
	return values
}
