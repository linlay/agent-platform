package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/kbase"
	"agent-platform/internal/rootpaths"
)

// WorkspaceChatAuditFinding describes one configuration that must be fixed
// before deploying the strict Workspace/Chat container protocol.
type WorkspaceChatAuditFinding struct {
	AgentKey   string `json:"agentKey,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
	SourcePath string `json:"sourcePath,omitempty"`
}

type workspaceChatAuditReference struct {
	owner      string
	sourcePath string
	targets    []string
}

// AuditWorkspaceChatConfig is read-only. In particular, it does not use the
// normal catalog loader because that loader may reconcile declared skills.
func AuditWorkspaceChatConfig(cfg config.Config) ([]WorkspaceChatAuditFinding, error) {
	root := strings.TrimSpace(cfg.Paths.AgentsDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agents directory %s: %w", root, err)
	}
	findings := make([]WorkspaceChatAuditFinding, 0)
	knownAgentKeys := make(map[string]struct{})
	agentReferences := make([]workspaceChatAuditReference, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !ShouldLoadRuntimeName(name) {
			continue
		}
		source, ok := runtimeAgentSource(root, name, entry)
		if !ok {
			continue
		}
		definition, readErr := readAdminAgentDefinitionMap(source.Path)
		if readErr != nil {
			continue
		}
		key := adminAgentKey(source, adminAgentFallbackKey(source), definition)
		if strings.TrimSpace(key) != "" {
			knownAgentKeys[strings.TrimSpace(key)] = struct{}{}
		}
		contextConfig := mapNode(definition["contextConfig"])
		runtimeConfig := mapNode(definition["runtimeConfig"])
		hasRuntimeSandbox := strings.TrimSpace(stringNode(runtimeConfig["environmentId"])) != ""
		if targets := listStrings(contextConfig["agents"]); len(targets) > 0 {
			agentReferences = append(agentReferences, workspaceChatAuditReference{
				owner:      key,
				sourcePath: source.Path,
				targets:    targets,
			})
		}
		findings = append(findings, auditReservedWorkspaceChatMounts(key, source.Path, definition)...)

		def, _, parseErr := parseAgentFileRaw(source.Path)
		if parseErr != nil {
			if workspaceChatMigrationError(parseErr.Error()) {
				findings = append(findings, workspaceChatFinding(key, "invalid_workspace_chat_config", parseErr, source.Path))
			}
			continue
		}
		if def.KBaseConfig.Enabled {
			if separationErr := kbase.ValidateWorkspaceChatsSeparation(def.Workspace.Root, cfg.Paths.ChatsDir); separationErr != nil {
				findings = append(findings, workspaceChatFinding(key, "kbase_workspace_chats_overlap", separationErr, source.Path))
				continue
			}
		}
		if strings.TrimSpace(def.Workspace.Root) == "" {
			continue
		}
		if workspaceErr := validateAgentWorkspace(def.Workspace); workspaceErr != nil {
			findings = append(findings, workspaceChatFinding(key, "invalid_workspace", workspaceErr, source.Path))
			continue
		}
		semanticRoots, separationErr := rootpaths.New(def.Workspace.Root, cfg.Paths.ChatsDir, "")
		if separationErr != nil {
			findings = append(findings, workspaceChatFinding(key, "workspace_chats_overlap", separationErr, source.Path))
		} else if masks, maskErr := semanticRoots.ContainerMaskedPaths(); hasRuntimeSandbox && maskErr == nil && len(masks) > 0 {
			findings = append(findings, WorkspaceChatAuditFinding{
				AgentKey:   strings.TrimSpace(key),
				Code:       "container_workspace_mask_required",
				Message:    fmt.Sprintf("dual-root-v2 must mask %s", strings.Join(masks, ", ")),
				Severity:   "info",
				SourcePath: filepath.Clean(source.Path),
			})
		}
	}
	findings = append(findings, auditOrphanAgentReferences(knownAgentKeys, agentReferences)...)
	teamReferences, teamErr := auditTeamReferences(cfg.Paths.TeamsDir)
	if teamErr != nil {
		return nil, teamErr
	}
	findings = append(findings, auditOrphanAgentReferences(knownAgentKeys, teamReferences)...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].SourcePath != findings[j].SourcePath {
			return findings[i].SourcePath < findings[j].SourcePath
		}
		if findings[i].AgentKey != findings[j].AgentKey {
			return findings[i].AgentKey < findings[j].AgentKey
		}
		return findings[i].Code < findings[j].Code
	})
	return findings, nil
}

func auditTeamReferences(teamsDir string) ([]workspaceChatAuditReference, error) {
	teamsDir = strings.TrimSpace(teamsDir)
	if teamsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(teamsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read teams directory %s: %w", teamsDir, err)
	}
	references := make([]workspaceChatAuditReference, 0)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !ShouldLoadRuntimeName(entry.Name()) {
			continue
		}
		sourcePath := resolveDirectoryTeamConfig(filepath.Join(teamsDir, entry.Name()))
		if sourcePath == "" {
			continue
		}
		definition, err := readAdminAgentDefinitionMap(sourcePath)
		if err != nil {
			continue
		}
		targets := listStrings(definition["agentKeys"])
		if len(targets) == 0 {
			continue
		}
		references = append(references, workspaceChatAuditReference{
			owner:      "team/" + entry.Name(),
			sourcePath: sourcePath,
			targets:    targets,
		})
	}
	return references, nil
}

func auditOrphanAgentReferences(knownAgentKeys map[string]struct{}, references []workspaceChatAuditReference) []WorkspaceChatAuditFinding {
	findings := make([]WorkspaceChatAuditFinding, 0)
	for _, reference := range references {
		for _, target := range reference.targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if _, ok := knownAgentKeys[target]; ok {
				continue
			}
			findings = append(findings, WorkspaceChatAuditFinding{
				AgentKey:   strings.TrimSpace(reference.owner),
				Code:       "orphan_agent_reference",
				Message:    fmt.Sprintf("referenced agent %q has no configuration", target),
				Severity:   "error",
				SourcePath: filepath.Clean(reference.sourcePath),
			})
		}
	}
	return findings
}

func workspaceChatFinding(agentKey string, code string, err error, sourcePath string) WorkspaceChatAuditFinding {
	return WorkspaceChatAuditFinding{
		AgentKey:   strings.TrimSpace(agentKey),
		Code:       strings.TrimSpace(code),
		Message:    err.Error(),
		Severity:   "error",
		SourcePath: filepath.Clean(sourcePath),
	}
}

func workspaceChatMigrationError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	for _, token := range []string{
		"runtimeconfig.workspaceroot",
		"kbaseconfig.source",
		"container hub sandbox agents",
	} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}

func auditReservedWorkspaceChatMounts(agentKey string, sourcePath string, definition map[string]any) []WorkspaceChatAuditFinding {
	runtimeConfig := mapNode(definition["runtimeConfig"])
	mounts := listMaps(runtimeConfig["sandboxMounts"])
	findings := make([]WorkspaceChatAuditFinding, 0)
	for index, mount := range mounts {
		destination := strings.TrimSpace(stringNode(mount["destination"]))
		if destination == "" {
			// Accept the spelling used by older examples so the migration audit
			// can flag it even though the runtime schema ignores it.
			destination = strings.TrimSpace(stringNode(mount["target"]))
		}
		destination = filepath.ToSlash(filepath.Clean(destination))
		if destination != "/workspace" && destination != "/chat" &&
			!strings.HasPrefix(destination, "/workspace/") &&
			!strings.HasPrefix(destination, "/chat/") {
			continue
		}
		findings = append(findings, WorkspaceChatAuditFinding{
			AgentKey:   strings.TrimSpace(agentKey),
			Code:       "reserved_sandbox_mount",
			Message:    fmt.Sprintf("runtimeConfig.sandboxMounts[%d] destination %q is reserved", index, destination),
			Severity:   "error",
			SourcePath: filepath.Clean(sourcePath),
		})
	}
	return findings
}
