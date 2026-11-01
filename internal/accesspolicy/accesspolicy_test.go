package accesspolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/temppaths"
)

func TestDefaultPolicyAllowsSkillCreationButProtectsSelectedSkill(t *testing.T) {
	workspace := t.TempDir()
	center := filepath.Join(workspace, "skills-center")
	selected := filepath.Join(center, "selected")
	if err := os.MkdirAll(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove} {
		t.Run(level, func(t *testing.T) {
			session := contracts.QuerySession{
				AccessLevel:   level,
				WorkspaceRoot: workspace,
				RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{
					WorkspaceDir:    workspace,
					SkillsCenterDir: center,
				}},
				RunAccessRoots: contracts.RunAccessRoots{ReadonlyRoots: []string{selected}},
			}
			cfg := config.AccessPolicyConfig{}
			created, err := BuildPathPlan(cfg, session, WriteAccess, "@skills-center/new-skill/SKILL.md")
			if err != nil || !created.Allowed() {
				t.Fatalf("new skill inside workspace should be writable: plan=%#v err=%v", created, err)
			}
			protected, err := BuildPathPlan(cfg, session, WriteAccess, "@skills-center/selected/SKILL.md")
			if err != nil || !protected.Blocked() {
				t.Fatalf("selected skill must remain readonly: plan=%#v err=%v", protected, err)
			}
			cfg.Levels = map[string]config.AccessPolicyLevelConfig{
				contracts.AccessLevelAskForApproval: {ReadonlyRoots: []string{"@skills-center"}},
			}
			explicit, err := BuildPathPlan(cfg, session, WriteAccess, "@skills-center/new-skill/SKILL.md")
			if err != nil || !explicit.Blocked() {
				t.Fatalf("explicit center readonly must still block writes: plan=%#v err=%v", explicit, err)
			}
		})
	}
}

func TestReadonlyRootsBlockWritesBeforeOutsideApprovalOrHostAccess(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	readonly := filepath.Join(root, "readonly")
	for _, dir := range []string{workspace, readonly} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	target := filepath.Join(readonly, "blocked.txt")
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelAskForApproval: {
			ReadRoots:     []string{"@workspace"},
			WriteRoots:    []string{"@workspace"},
			ReadonlyRoots: []string{readonly},
			Approvals: config.AccessPolicyApprovalConfig{
				WriteOutsideRoots: "hitl",
			},
		},
	}}

	for _, session := range []contracts.QuerySession{
		{AccessLevel: contracts.AccessLevelAskForApproval, WorkspaceRoot: workspace},
		{
			AccessLevel:   contracts.AccessLevelAskForApproval,
			WorkspaceRoot: workspace,
			RuntimeHostAccess: contracts.HostAccessRoots{
				WriteRoots: []string{readonly},
			},
		},
	} {
		plan, err := BuildPathPlan(cfg, session, WriteAccess, target)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Blocked() || plan.RequiresApproval() || !strings.Contains(plan.Reason, "readonly") {
			t.Fatalf("readonly path must be hard blocked before approval or host access: %#v", plan)
		}
	}
}

func TestRunAccessRootsAllowOnlySelectedSkillReadsAndRemainReadonly(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	center := filepath.Join(root, "skills-center")
	selected := filepath.Join(center, "selected")
	sibling := filepath.Join(center, "sibling")
	for _, dir := range []string{workspace, selected, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	selectedFile := filepath.Join(selected, "SKILL.md")
	siblingFile := filepath.Join(sibling, "SKILL.md")
	for _, path := range []string{selectedFile, siblingFile} {
		if err := os.WriteFile(path, []byte("skill"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelAskForApproval: {
			ReadRoots:     []string{"@workspace"},
			WriteRoots:    []string{"@workspace"},
			ReadonlyRoots: []string{},
			Approvals: config.AccessPolicyApprovalConfig{
				ReadOutsideRoots:  "hitl",
				WriteOutsideRoots: "hitl",
			},
		},
	}}
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAskForApproval,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{
			WorkspaceDir:    workspace,
			SkillsCenterDir: center,
		}},
		RunAccessRoots: contracts.RunAccessRoots{
			ReadRoots:     []string{selected},
			ReadonlyRoots: []string{selected},
		},
	}

	selectedRead, err := BuildPathPlan(cfg, session, ReadAccess, "@skills-center/selected/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !selectedRead.Allowed() || selectedRead.RequiresApproval() {
		t.Fatalf("selected skill read should be allowed: %#v", selectedRead)
	}
	siblingRead, err := BuildPathPlan(cfg, session, ReadAccess, "@skills-center/sibling/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !siblingRead.RequiresApproval() || siblingRead.Allowed() {
		t.Fatalf("unselected sibling read should retain outside-root approval: %#v", siblingRead)
	}
	selectedWrite, err := BuildPathPlan(cfg, session, WriteAccess, "@skills-center/selected/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !selectedWrite.Blocked() || selectedWrite.RequiresApproval() {
		t.Fatalf("selected skill write should be hard blocked: %#v", selectedWrite)
	}

	fullAccess := session
	fullAccess.AccessLevel = contracts.AccessLevelFullAccess
	selectedFullWrite, err := BuildPathPlan(cfg, fullAccess, WriteAccess, selectedFile)
	if err != nil {
		t.Fatal(err)
	}
	if !selectedFullWrite.Blocked() {
		t.Fatalf("run-scoped readonly must survive full_access: %#v", selectedFullWrite)
	}
}

func TestRunAccessRootDoesNotFollowSelectedSkillSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not portable on Windows")
	}
	root := t.TempDir()
	center := filepath.Join(root, "skills-center")
	selected := filepath.Join(center, "selected")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{selected, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(selected, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	session := contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{SkillsCenterDir: center}},
		RunAccessRoots: contracts.RunAccessRoots{ReadRoots: []string{selected}, ReadonlyRoots: []string{selected}},
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelAskForApproval: {ReadRoots: []string{}, Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "hitl"}},
	}}
	plan, err := BuildPathPlan(cfg, session, ReadAccess, "@skills-center/selected/escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresApproval() || plan.Path != realPathForTest(t, outsideFile) {
		t.Fatalf("symlink escape must be reclassified outside the selected skill: %#v", plan)
	}
}

func TestSandboxTmpPathMapsToFrozenHostTempRoot(t *testing.T) {
	workspace := t.TempDir()
	tempRoot := t.TempDir()
	session := contracts.QuerySession{
		WorkspaceRoot:          workspace,
		TempRoot:               tempRoot,
		TempRoots:              []string{tempRoot},
		AgentHasRuntimeSandbox: true,
		RuntimeContext: contracts.RuntimeRequestContext{
			SandboxPaths: contracts.SandboxPaths{WorkspaceDir: "/workspace", ChatDir: "/chat"},
		},
	}
	plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, WriteAccess, "/tmp/nested/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realPathForTest(t, tempRoot), "nested", "note.txt")
	if !plan.Allowed() || plan.Path != want {
		t.Fatalf("sandbox /tmp path was not mapped to frozen host temporary root: %#v want=%q", plan, want)
	}
}

func withSystemTempRoots(session contracts.QuerySession) contracts.QuerySession {
	resolver := temppaths.System()
	if primary, ok := resolver.Primary(); ok {
		session.TempRoot = primary.Host
	}
	session.TempRoots = resolver.Paths()
	return session
}

func TestDefaultLevelAllowsWorkspaceAgentAndSkillsRead(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	agent := filepath.Join(root, "agent")
	skills := filepath.Join(agent, "skills")
	center := filepath.Join(root, "skills-center")
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAskForApproval,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:    workspace,
				AgentDir:        agent,
				SkillsDir:       skills,
				SkillsCenterDir: center,
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

	plan, err := BuildPathPlan(cfg, session, ReadAccess, filepath.Join(center, "shared", "SKILL.md"))
	if err != nil {
		t.Fatalf("build center read plan: %v", err)
	}
	if !plan.RequiresApproval() {
		t.Fatalf("expected skills-center read approval, got %#v", plan)
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
		AccessLevel:   contracts.AccessLevelAskForApproval,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: workspace,
				ChatDir:      chatDir,
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

func TestResolveSessionPathWithoutWorkspaceRequiresExplicitSemanticRoot(t *testing.T) {
	chatDir := t.TempDir()
	session := contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: chatDir},
		},
	}

	resolved, err := ResolveSessionPath(session, "@chat/upload.txt")
	if err != nil {
		t.Fatalf("resolve explicit Chat path: %v", err)
	}
	if want := filepath.Join(chatDir, "upload.txt"); resolved != want {
		t.Fatalf("resolved Chat path = %q, want %q", resolved, want)
	}
	for _, rawPath := range []string{"upload.txt", "@workspace/upload.txt"} {
		if _, err := ResolveSessionPath(session, rawPath); err == nil ||
			!strings.Contains(err.Error(), "workspace_unavailable") ||
			!strings.Contains(err.Error(), "@chat") {
			t.Fatalf("ResolveSessionPath(%q) error = %v, want actionable workspace_unavailable", rawPath, err)
		}
	}
}

func TestWorkspaceContainingChatsUsesChatFirstSemanticClassification(t *testing.T) {
	workspace := t.TempDir()
	chatsDir := filepath.Join(workspace, "runtime", "chats")
	chatDir := filepath.Join(chatsDir, "chat-1")
	otherChatDir := filepath.Join(chatsDir, "chat-2")
	for _, dir := range []string{chatDir, otherChatDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	session := contracts.QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: workspace,
				ChatsDir:     chatsDir,
				ChatDir:      chatDir,
			},
		},
	}
	currentChatFile := filepath.Join(chatDir, "upload.txt")
	if PathInSessionWorkspace(session, currentChatFile) {
		t.Fatal("current chat file must not be classified as workspace")
	}
	if !PathInSessionChat(session, currentChatFile) {
		t.Fatal("current chat file must be classified as current chat")
	}
	currentPlan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, currentChatFile)
	if err != nil || !currentPlan.Allowed() {
		t.Fatalf("current chat access = %#v, %v", currentPlan, err)
	}
	otherPlan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, filepath.Join(otherChatDir, "private.txt"))
	if err != nil || !otherPlan.RequiresApproval() {
		t.Fatalf("other chat access = %#v, %v, want HITL", otherPlan, err)
	}
	for _, rawPath := range []string{
		"runtime/chats/chat-1/upload.txt",
		"@workspace/runtime/chats/chat-1/upload.txt",
	} {
		if _, err := ResolveSessionPath(session, rawPath); err == nil ||
			!strings.Contains(err.Error(), "path_crosses_chat_root") {
			t.Fatalf("ResolveSessionPath(%q) error = %v", rawPath, err)
		}
	}
}

func TestBuildPathPlanCanonicalKeysStableAcrossEquivalentForms(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAskForApproval,
		WorkspaceRoot: workspace,
	}

	relativePlan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, "notes.txt")
	if err != nil {
		t.Fatalf("build relative plan: %v", err)
	}
	absolutePlan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, filepath.Join(workspace, ".", "notes.txt"))
	if err != nil {
		t.Fatalf("build absolute plan: %v", err)
	}
	if relativePlan.Path != absolutePlan.Path || relativePlan.Path != realPathForTest(t, path) {
		t.Fatalf("expected host paths to remain stable, relative=%#v absolute=%#v", relativePlan, absolutePlan)
	}
	if relativePlan.CommandText != "file_read "+relativePlan.Path {
		t.Fatalf("expected command text to use host path, got %#v", relativePlan)
	}
	if relativePlan.Fingerprint != absolutePlan.Fingerprint || relativePlan.RuleKey != absolutePlan.RuleKey {
		t.Fatalf("expected equivalent path forms to share canonical approval keys, relative=%#v absolute=%#v", relativePlan, absolutePlan)
	}
}

func TestAutoApproveAndFullAccessLevels(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chatDir := filepath.Join(root, "chats", "chat-1")
	outside := filepath.Join(root, "outside", "secret.txt")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	cfg := config.AccessPolicyConfig{}

	autoSession := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: workspace,
				ChatDir:      chatDir,
			},
		},
	}
	workspaceWritePlan, err := BuildPathPlan(cfg, autoSession, WriteAccess, filepath.Join(workspace, "artifact.md"))
	if err != nil {
		t.Fatalf("build auto workspace write plan: %v", err)
	}
	if !workspaceWritePlan.Allowed() || workspaceWritePlan.RequiresApproval() {
		t.Fatalf("expected auto-approve workspace write allowed, got %#v", workspaceWritePlan)
	}
	chatWritePlan, err := BuildPathPlan(cfg, autoSession, WriteAccess, filepath.Join(chatDir, "artifact.md"))
	if err != nil {
		t.Fatalf("build auto chat write plan: %v", err)
	}
	if !chatWritePlan.Allowed() || chatWritePlan.RequiresApproval() {
		t.Fatalf("expected auto-approve chat write allowed, got %#v", chatWritePlan)
	}
	autoPlan, err := BuildPathPlan(cfg, autoSession, ReadAccess, outside)
	if err != nil {
		t.Fatalf("build auto read plan: %v", err)
	}
	if !autoPlan.AutoApproved() {
		t.Fatalf("expected auto-approved outside read, got %#v", autoPlan)
	}
	autoWritePlan, err := BuildPathPlan(cfg, autoSession, WriteAccess, outside)
	if err != nil {
		t.Fatalf("build auto write plan: %v", err)
	}
	if !autoWritePlan.RequiresApproval() {
		t.Fatalf("expected auto-approve outside write to require approval, got %#v", autoWritePlan)
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

func realPathForTest(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}

func TestRuntimeAliasResolvesWithoutGrantingAccess(t *testing.T) {
	runtimeHome := t.TempDir()
	chatsDir := filepath.Join(runtimeHome, "chats")
	chatDir := filepath.Join(chatsDir, "chat-1")
	stateDir := filepath.Join(runtimeHome, ".state")
	otherChatDir := filepath.Join(chatsDir, "chat-2")
	for _, dir := range []string{chatDir, otherChatDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	session := contracts.QuerySession{
		ProtectedPaths: []string{stateDir},
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{RuntimeHome: runtimeHome, ChatsDir: chatsDir, ChatDir: chatDir},
		},
	}

	resolved, err := ResolveSessionPath(session, "@runtime/chats/chat-1/a.txt")
	if err != nil || resolved != filepath.Join(chatDir, "a.txt") {
		t.Fatalf("ResolveSessionPath = %q, %v", resolved, err)
	}
	if _, err := ResolveSessionPath(session, "@runtime/../outside"); err == nil {
		t.Fatal("expected @runtime escape to be rejected")
	}
	if _, err := ResolveSessionPath(contracts.QuerySession{}, "@runtime/chats"); err == nil {
		t.Fatal("expected @runtime to be unavailable without a runtime root")
	}

	cfg := config.DefaultAccessPolicyConfig()
	for rawPath, want := range map[string]Decision{
		"@runtime/chats/chat-1/a.txt": DecisionAllow,
		"@runtime/chats/chat-2/a.txt": DecisionRequiresApproval,
		"@runtime/.state/secret":      DecisionBlock,
	} {
		plan, err := BuildPathPlan(cfg, session, WriteAccess, rawPath)
		if err != nil {
			t.Fatalf("BuildPathPlan(%q): %v", rawPath, err)
		}
		absolute, err := BuildPathPlan(cfg, session, WriteAccess, filepath.Join(runtimeHome, filepath.FromSlash(strings.TrimPrefix(rawPath, "@runtime/"))))
		if err != nil {
			t.Fatalf("BuildPathPlan absolute for %q: %v", rawPath, err)
		}
		if plan.Decision != want || plan.Decision != absolute.Decision {
			t.Fatalf("%q decision = %v, absolute = %v, want %v", rawPath, plan.Decision, absolute.Decision, want)
		}
	}
}
