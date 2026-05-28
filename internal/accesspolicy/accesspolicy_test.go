package accesspolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestDefaultLevelAllowsWorkspaceAgentAndSkillsRead(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	agent := filepath.Join(root, "agent")
	skills := filepath.Join(agent, "skills")
	market := filepath.Join(root, "skills-market")
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:    workspace,
				AgentDir:        agent,
				SkillsDir:       skills,
				SkillsMarketDir: market,
			},
		},
	}
	cfg := config.AccessPolicyConfig{}

	for _, path := range []string{
		filepath.Join(workspace, "notes.md"),
		filepath.Join(agent, "AGENTS.md"),
		filepath.Join(skills, "tool", "SKILL.md"),
	} {
		plan, err := BuildPathPlan(cfg, session, ReadAccess, path)
		if err != nil {
			t.Fatalf("build read plan for %s: %v", path, err)
		}
		if !plan.Allowed() || plan.RequiresApproval() {
			t.Fatalf("expected read allowed for %s, got %#v", path, plan)
		}
	}

	plan, err := BuildPathPlan(cfg, session, ReadAccess, filepath.Join(market, "shared", "SKILL.md"))
	if err != nil {
		t.Fatalf("build market read plan: %v", err)
	}
	if !plan.RequiresApproval() {
		t.Fatalf("expected skills-market read approval, got %#v", plan)
	}
}

func TestDefaultLevelAllowsChatReadWriteWithExplicitWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chatDir := filepath.Join(root, "chats", "chat-1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       workspace,
				ChatAttachmentsDir: chatDir,
			},
		},
	}
	cfg := config.AccessPolicyConfig{}
	chatFile := filepath.Join(chatDir, "artifact.md")

	readPlan, err := BuildPathPlan(cfg, session, ReadAccess, chatFile)
	if err != nil {
		t.Fatalf("build chat read plan: %v", err)
	}
	if !readPlan.Allowed() || readPlan.RequiresApproval() {
		t.Fatalf("expected chat read allowed, got %#v", readPlan)
	}

	writePlan, err := BuildPathPlan(cfg, session, WriteAccess, chatFile)
	if err != nil {
		t.Fatalf("build chat write plan: %v", err)
	}
	if !writePlan.Allowed() || writePlan.RequiresApproval() {
		t.Fatalf("expected chat write allowed, got %#v", writePlan)
	}
}

func TestAutoApproveAndFullAccessLevels(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside", "secret.txt")
	cfg := config.AccessPolicyConfig{}

	autoSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelAutoApprove, WorkspaceRoot: workspace}
	autoPlan, err := BuildPathPlan(cfg, autoSession, ReadAccess, outside)
	if err != nil {
		t.Fatalf("build auto read plan: %v", err)
	}
	if !autoPlan.AutoApproved() {
		t.Fatalf("expected auto-approved outside read, got %#v", autoPlan)
	}

	fullSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelFullAccess, WorkspaceRoot: workspace}
	fullPlan, err := BuildPathPlan(cfg, fullSession, WriteAccess, outside)
	if err != nil {
		t.Fatalf("build full write plan: %v", err)
	}
	if !fullPlan.Allowed() || fullPlan.RequiresApproval() {
		t.Fatalf("expected full-access write allowed, got %#v", fullPlan)
	}
}

func TestBashAccessPolicyDefaultCwdAndPathDecisions(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	session := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, WorkspaceRoot: workspace}
	cfg := config.AccessPolicyConfig{}

	allowed := ReviewBashCommand(cfg, session, "cat ./notes.txt", workspace, nil)
	if !allowed.Allowed() || allowed.RequiresApproval() {
		t.Fatalf("expected workspace relative bash path allowed, got %#v", allowed)
	}

	cwdOutside := ReviewBashCommand(cfg, session, "pwd", outside, nil)
	if !cwdOutside.RequiresApproval() {
		t.Fatalf("expected outside cwd approval, got %#v", cwdOutside)
	}

	outsidePath := filepath.Join(outside, "secret.txt")
	bashPlan := ReviewBashCommand(cfg, session, "cat "+outsidePath, workspace, nil)
	if !bashPlan.RequiresApproval() {
		t.Fatalf("expected outside bash path approval, got %#v", bashPlan)
	}
	filePlan, err := BuildPathPlan(cfg, session, ReadAccess, outsidePath)
	if err != nil {
		t.Fatalf("build file path plan: %v", err)
	}
	if bashPlan.Decision != filePlan.Decision {
		t.Fatalf("expected bash and file path decisions to match, bash=%#v file=%#v", bashPlan, filePlan)
	}
}

func TestBashAccessPolicyAllowsChatWriteRoot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chatDir := filepath.Join(root, "chats", "chat-1")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       workspace,
				ChatAttachmentsDir: chatDir,
			},
		},
	}

	plan := ReviewBashCommand(config.AccessPolicyConfig{}, session, "touch "+filepath.Join(chatDir, "artifact.md"), workspace, nil)
	if !plan.Allowed() || plan.RequiresApproval() {
		t.Fatalf("expected chat bash write allowed, got %#v", plan)
	}
}

func TestBashAccessPolicyComplexAndOpaqueLevels(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.AccessPolicyConfig{}

	defaultSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, WorkspaceRoot: workspace}
	complex := ReviewBashCommand(cfg, defaultSession, "cat $TARGET", workspace, nil)
	if !complex.RequiresApproval() || complex.RuleKey != "bash-access:complex" {
		t.Fatalf("expected complex bash approval, got %#v", complex)
	}
	opaque := ReviewBashCommand(cfg, defaultSession, "npm test", workspace, nil)
	if !opaque.RequiresApproval() {
		t.Fatalf("expected opaque bash approval, got %#v", opaque)
	}

	autoSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelAutoApprove, WorkspaceRoot: workspace}
	autoOpaque := ReviewBashCommand(cfg, autoSession, "npm test", workspace, nil)
	if !autoOpaque.AutoApproved() {
		t.Fatalf("expected opaque bash auto approval, got %#v", autoOpaque)
	}

	fullSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelFullAccess, WorkspaceRoot: workspace}
	fullComplex := ReviewBashCommand(cfg, fullSession, "cat $TARGET", workspace, nil)
	if !fullComplex.Allowed() || fullComplex.RequiresApproval() {
		t.Fatalf("expected full access complex bash allowed, got %#v", fullComplex)
	}
}

func TestBashWriteInWriteRootsApprovalAction(t *testing.T) {
	workspace := t.TempDir()
	session := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, WorkspaceRoot: workspace}

	defaultPlan := ReviewBashCommand(config.AccessPolicyConfig{}, session, "touch ./created.txt", workspace, nil)
	if !defaultPlan.Allowed() || defaultPlan.AutoApproved() {
		t.Fatalf("expected default workspace bash write allowed, got %#v", defaultPlan)
	}

	cfg := config.AccessPolicyConfig{
		Levels: map[string]config.AccessPolicyLevelConfig{
			contracts.AccessLevelDefault: {
				ReadRoots:  []string{"@workspace"},
				WriteRoots: []string{"@workspace"},
				Approvals: config.AccessPolicyApprovalConfig{
					ReadOutsideRoots:      "hitl",
					WriteOutsideRoots:     "hitl",
					BashComplexFilesystem: "hitl",
					BashOpaqueCommand:     "hitl",
					BashWriteInWriteRoots: "hitl",
				},
			},
		},
	}
	approvalPlan := ReviewBashCommand(cfg, session, "touch ./created.txt", workspace, nil)
	if !approvalPlan.RequiresApproval() || !strings.HasPrefix(approvalPlan.RuleKey, "bash-access:write-root:") {
		t.Fatalf("expected write-root bash approval, got %#v", approvalPlan)
	}

	cfg.Levels[contracts.AccessLevelDefault] = config.AccessPolicyLevelConfig{
		ReadRoots:  []string{"@workspace"},
		WriteRoots: []string{"@workspace"},
		Approvals: config.AccessPolicyApprovalConfig{
			ReadOutsideRoots:      "hitl",
			WriteOutsideRoots:     "hitl",
			BashComplexFilesystem: "hitl",
			BashOpaqueCommand:     "hitl",
			BashWriteInWriteRoots: "auto",
		},
	}
	autoPlan := ReviewBashCommand(cfg, session, "touch ./created.txt", workspace, nil)
	if !autoPlan.AutoApproved() {
		t.Fatalf("expected write-root bash auto approval, got %#v", autoPlan)
	}
}
