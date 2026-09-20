package server

import (
	"log"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/skillsexec"
)

// Only resolved configured skills and this query's selected extras contribute
// roots. Connector skills retain their separate mounted-CLI authorization.
func buildSkillScriptScope(session contracts.QuerySession, def catalog.AgentDefinition, selected []resolvedMustUseSkill) *skillsexec.Scope {
	roots := []skillsexec.Root{}
	for _, key := range def.Skills {
		if def.IsConnectorSkill(key) || strings.TrimSpace(def.RuntimeDir) == "" {
			continue
		}
		root, err := resolveMustUseSkillRoot(filepath.Join(def.RuntimeDir, "skills"), key)
		if err != nil {
			log.Printf("[server][skill-execution] agent=%s skill=%s: execution grant unavailable: %v", def.Key, key, err)
			continue
		}
		guest := ""
		if session.AgentHasRuntimeSandbox && session.RuntimeContext.SandboxPaths.SkillsDir != "" {
			guest = path.Join(session.RuntimeContext.SandboxPaths.SkillsDir, key)
		}
		roots = append(roots, skillsexec.Root{Host: root, Guest: guest})
	}
	for _, skill := range selected {
		if def.IsConnectorSkill(skill.Key) {
			continue
		}
		guest := ""
		if session.AgentHasRuntimeSandbox {
			base := session.RuntimeContext.SandboxPaths.SkillsDir
			if skill.Extra {
				base = session.RuntimeContext.SandboxPaths.SkillsCenterDir
			}
			if base != "" {
				guest = path.Join(base, skill.Key)
			}
		}
		roots = append(roots, skillsexec.Root{Host: skill.RootPath, Guest: guest})
	}
	ctx := contracts.ExecutionContext{Session: session}
	return skillsexec.New(ctx.ScriptOwner(), roots)
}
