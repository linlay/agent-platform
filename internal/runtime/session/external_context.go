package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

// PrepareExternalContext shares trusted resource/catalog resolution without
// inheriting the Native model, history, system prompt or tool loop.
func (b *Builder) PrepareExternalContext(ctx context.Context, req runtimetypes.QueryCommand, def catalog.AgentDefinition, session *contracts.QuerySession) error {
	// The ACP-facing MCP server already supplies discovery; expose the mounted
	// tools directly rather than nesting the Native model's MCP search gateway.
	session.ToolNames, session.MCPServerKeys = SessionMCPTools(EffectiveAgentTools(def), def, false, func() []string {
		return McpToolNamesForServers(b.deps.Tools, def.ConnectorMCPServers)
	})
	session.ToolNames = RuntimeModeToolNames(session.ToolNames, b.deps.Config.RuntimeMode)
	session.NativeConnectorTools = RuntimeNativeConnectorTools(def)
	session.ConnectorDirs = RuntimeConnectorDirs(def)
	policy := def.Interaction()
	for _, ref := range req.References {
		if err := policy.ValidateReference(ref.Type); err != nil {
			return err
		}
	}
	ids := append(append([]string{}, def.Skills...), queryinput.ResourceIDs(req.References, "skill")...)
	resolved, err := b.ResolveSkills(def, ids)
	if err != nil {
		return ReferencedSkillUnavailableStatus(err)
	}
	grant, err := b.PrepareRunReferences(ctx, *session, def, req.References, false, false, false)
	if err != nil {
		return err
	}
	session.ReferencesScope = contracts.NewRunReferences(nil)
	if err := session.ReferencesScope.Commit(grant); err != nil {
		return err
	}
	for _, ref := range grant.References {
		if ref.Type == "kbase" {
			data, _ := json.Marshal(ref)
			session.CapabilityPrompts = append(session.CapabilityPrompts, "Selected knowledge library (use the mounted kbase tools to retrieve evidence):\n"+string(data))
		}
	}
	var total int64
	for _, skill := range def.ConnectorSkills {
		resolved.Skills = append(resolved.Skills, ReferencedSkill{ID: skill.ID, RootPath: skill.RuntimeDir})
	}
	if len(session.ConnectorDirs) > 0 {
		session.CapabilityPrompts = append(session.CapabilityPrompts, "Platform connectors are available via the platform MCP server. Call platform_tools each turn to discover the current mounted tools and CLI connector IDs. Use platform_call for tools and platform_connector with argv for CLI commands. Credentials stay on Platform; do not bypass these tools with local shell commands.")
	}
	for _, skill := range resolved.Skills {
		root, err := os.OpenRoot(skill.RootPath)
		if err != nil {
			return err
		}
		f, err := root.Open("SKILL.md")
		root.Close()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)-total+1))
		f.Close()
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > 1<<20 {
			return fmt.Errorf("referenced_skill_unavailable: skill instructions exceed 1 MiB")
		}
		session.CapabilityPrompts = append(session.CapabilityPrompts, "Selected skill "+skill.ID+" (base directory: "+filepath.Clean(skill.RootPath)+"):\n"+strings.TrimSpace(string(data)))
	}
	return nil
}
