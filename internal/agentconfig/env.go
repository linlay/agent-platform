// Package agentconfig defines the environment contract for an agent-owned
// XDG-style configuration directory.
package agentconfig

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// EnvConfigHome is the standard XDG configuration root presented to an
	// agent process. Tools normally append their own name below this directory.
	EnvConfigHome = "XDG_CONFIG_HOME"
	// EnvAgentConfigHome lets tools explicitly identify the agent-owned root.
	EnvAgentConfigHome = "AP_AGENT_CONFIG_HOME"
	// EnvSystemConfigHome retains the process's pre-injection XDG root so tools
	// that support overlay lookup can fall back to it.
	EnvSystemConfigHome = "AP_SYSTEM_XDG_CONFIG_HOME"
)

// Environment returns process overrides for an agent directory. An absent
// directory is intentionally valid: tools then see an empty primary directory
// and can fall back to their normal system configuration.
func Environment(agentDir string) map[string]string {
	agentDir = strings.TrimSpace(agentDir)
	if agentDir == "" {
		return nil
	}
	return environment(filepath.Join(filepath.Clean(agentDir), ".config"))
}

// ContainerEnvironment is the equivalent for a Linux container path. It must
// not use filepath.Join because the platform process may run on Windows.
func ContainerEnvironment(agentDir string) map[string]string {
	agentDir = strings.TrimSpace(agentDir)
	if agentDir == "" {
		return nil
	}
	return environment(path.Join(agentDir, ".config"))
}

func environment(configHome string) map[string]string {
	env := map[string]string{
		EnvConfigHome:      configHome,
		EnvAgentConfigHome: configHome,
	}
	if systemConfigHome := strings.TrimSpace(os.Getenv(EnvConfigHome)); systemConfigHome != "" {
		env[EnvSystemConfigHome] = systemConfigHome
	}
	return env
}

// Merge applies maps from left to right. It is used to keep the established
// precedence of platform defaults < agent/skill env < invocation env.
func Merge(values ...map[string]string) map[string]string {
	var merged map[string]string
	for _, values := range values {
		for key, value := range values {
			if merged == nil {
				merged = make(map[string]string)
			}
			merged[key] = value
		}
	}
	return merged
}
