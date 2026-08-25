package llm

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestMustUseSkillRunAccessSkipsReadHITLAndBlocksMutation(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	skills := filepath.Join(root, "skills")
	selected := filepath.Join(skills, "selected")
	sibling := filepath.Join(skills, "sibling")
	for _, dir := range []string{workspace, selected, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, path := range []string{filepath.Join(selected, "SKILL.md"), filepath.Join(sibling, "SKILL.md")} {
		if err := os.WriteFile(path, []byte("skill"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	session := contracts.QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{
			WorkspaceDir: workspace,
			SkillsDir:    skills,
		}},
		RunAccessRoots: contracts.RunAccessRoots{
			ReadRoots:     []string{selected},
			ReadonlyRoots: []string{selected},
		},
	}
	stream := &llmRunStream{
		session: session,
		engine: &LLMAgentEngine{cfg: config.Config{AccessPolicy: config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
			contracts.AccessLevelDefault: {
				ReadRoots:     []string{"@workspace"},
				WriteRoots:    []string{"@workspace"},
				ReadonlyRoots: []string{},
				Approvals: config.AccessPolicyApprovalConfig{
					ReadOutsideRoots:  "hitl",
					WriteOutsideRoots: "hitl",
				},
			},
		}}}},
		execCtx: &contracts.ExecutionContext{Session: session},
	}

	selectedRead := &preparedToolInvocation{toolName: "file_read", args: map[string]any{"file_path": "@skills/selected/SKILL.md"}}
	selectedPlan := stream.lookupFileAccessPlan(selectedRead)
	if selectedPlan == nil || selectedPlan.Blocked || stream.fileAccessPlanNeedsApproval(*selectedPlan) {
		t.Fatalf("selected skill read should not need HITL: %#v", selectedPlan)
	}
	siblingRead := &preparedToolInvocation{toolName: "file_read", args: map[string]any{"file_path": "@skills/sibling/SKILL.md"}}
	siblingPlan := stream.lookupFileAccessPlan(siblingRead)
	if siblingPlan == nil || !stream.fileAccessPlanNeedsApproval(*siblingPlan) {
		t.Fatalf("unselected sibling should still need HITL: %#v", siblingPlan)
	}
	selectedWrite := &preparedToolInvocation{toolName: "file_write", args: map[string]any{
		"file_path": "@skills/selected/SKILL.md",
		"content":   "changed",
	}}
	writePlan := stream.lookupFileAccessPlan(selectedWrite)
	if writePlan == nil || !writePlan.Blocked || stream.fileAccessPlanNeedsApproval(*writePlan) {
		t.Fatalf("selected skill mutation must be blocked without unusable HITL: %#v", writePlan)
	}
}
