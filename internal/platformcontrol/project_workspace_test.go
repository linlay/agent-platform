package platformcontrol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/kbases"
)

func TestCatalogProjectRequiresWorkspaceBeforeValidationApprovalAndApply(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		t.Run(mode, func(t *testing.T) {
			h, libraries := knowledgeCatalogHandler(t)
			library, err := libraries.Create(kbases.Input{Name: "Docs", SourcePath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			prefix := fmt.Sprintf("key: project-agent\nmode: %s\nmodelConfig:\n  modelKey: test\nkbaseConfig:\n  libraryId: %s\n", mode, library.ID)
			if mode == "TEAM" {
				prefix += "teamConfig:\n  members: [member]\n"
			}
			for _, tc := range []struct{ name, runtime string }{
				{"missing runtime", ""},
				{"missing Workspace", "runtimeConfig: {}\n"},
				{"null Workspace", "runtimeConfig:\n  workspaceRoot: null\n"},
				{"empty Workspace", "runtimeConfig:\n  workspaceRoot: ''\n"},
				{"blank Workspace", "runtimeConfig:\n  workspaceRoot: '   '\n"},
				{"boolean Workspace", "runtimeConfig:\n  workspaceRoot: true\n"},
				{"root alias", "runtimeConfig:\n  workspaceRoot: '@root'\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, level := range []string{"default", "auto_approve", "full_access"} {
						t.Run(level, func(t *testing.T) {
							execution := controlExecution()
							execution.AccessLevel = level
							params := map[string]any{"resourceType": "agent", "resourceKey": "project-agent", "isProject": true, "content": prefix + tc.runtime}
							validation, err := h.Invoke(context.Background(), "catalog_query", map[string]any{"action": "validate", "args": params}, execution)
							if err != nil || validation.Error != "" || validation.Structured["valid"] != false {
								t.Fatalf("project without Workspace validated: %+v, %v", validation, err)
							}
							if !strings.Contains(validation.Output, "workspaceRoot") {
								t.Fatalf("project was rejected for an unrelated reason: %s", validation.Output)
							}
							args := map[string]any{"action": "apply", "args": params}
							if approval, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution); err == nil || approval != nil || !strings.Contains(err.Error(), "workspaceRoot") {
								t.Fatalf("invalid project reached approval: %+v, %v", approval, err)
							}
							result, err := h.Invoke(context.Background(), "catalog_manage", args, execution)
							if err != nil || result.Error != "control_failed" {
								t.Fatalf("invalid project was not blocked before approval: %+v, %v", result, err)
							}
							if !strings.Contains(result.Output, "workspaceRoot") {
								t.Fatalf("project apply failed for an unrelated reason: %s", result.Output)
							}
							if _, err := os.Stat(filepath.Join(h.cfg.Paths.AgentsDir, "project-agent")); !os.IsNotExist(err) {
								t.Fatalf("invalid project wrote an Agent directory: %v", err)
							}
							if _, found := h.registry.AgentDefinition("project-agent"); found {
								t.Fatal("invalid project was published to the catalog")
							}
						})
					}
				})
			}
		})
	}
}
