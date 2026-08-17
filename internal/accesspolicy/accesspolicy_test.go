package accesspolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/temppaths"
)

func TestTempRootIsEffectiveForFileAndSimpleBashPaths(t *testing.T) {
	workspace := t.TempDir()
	session := withSystemTempRoots(contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
	})
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
					BashWriteInWriteRoots: "allow",
				},
			},
		},
	}
	primary, ok := temppaths.System().Primary()
	if !ok {
		t.Fatal("system temporary root is unavailable")
	}
	absolute := filepath.Join(primary.Host, "agent-platform-access-policy", "note.txt")
	for _, rawPath := range []string{"@temp/agent-platform-access-policy/note.txt", absolute} {
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			plan, err := BuildPathPlan(cfg, session, mode, rawPath)
			if err != nil {
				t.Fatalf("BuildPathPlan(%s, %q): %v", mode, rawPath, err)
			}
			if !plan.Allowed() || plan.RequiresApproval() {
				t.Fatalf("temporary %s path should be allowed: %#v", mode, plan)
			}
		}
	}

	for _, command := range []string{
		"mkdir " + filepath.Dir(absolute),
		"touch " + absolute,
		"cp " + absolute + " " + absolute + ".copy",
		"mv " + absolute + ".copy " + absolute + ".renamed",
		"chmod 600 " + absolute,
		"rm " + absolute + ".renamed",
	} {
		plan := ReviewBashCommand(cfg, session, command, workspace, nil)
		if !plan.Allowed() || plan.RequiresApproval() {
			t.Fatalf("temporary bash path should not require path approval for %q: %#v", command, plan)
		}
	}
	opaque := ReviewBashCommand(cfg, session, "python3 "+absolute, workspace, nil)
	if !opaque.RequiresApproval() || !strings.Contains(opaque.RuleKey, "bash-access:opaque") {
		t.Fatalf("temporary script execution must keep opaque-command approval: %#v", opaque)
	}
}

func TestChatAndTempScriptExecutionRespectsAccessLevel(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chatDir := filepath.Join(root, "chats", "chat-1")
	tempRoot := filepath.Join(root, "temp")
	for _, dir := range []string{workspace, chatDir, tempRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	baseSession := contracts.QuerySession{
		WorkspaceRoot: workspace,
		ChatRoot:      chatDir,
		TempRoot:      tempRoot,
		TempRoots:     []string{tempRoot},
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: workspace,
				ChatDir:      chatDir,
			},
			SandboxPaths: contracts.SandboxPaths{
				WorkspaceDir: "/workspace",
				ChatDir:      "/chat",
			},
		},
	}

	tests := []struct {
		name    string
		sandbox bool
		cwd     string
		command string
	}{
		{name: "host chat python", cwd: "@chat", command: "python3 task.py"},
		{name: "host temp node", cwd: "@temp", command: "node task.js"},
		{name: "sandbox chat python", sandbox: true, cwd: "/chat", command: "python3 task.py"},
		{name: "sandbox temp node", sandbox: true, cwd: "/tmp", command: "node task.js"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defaultSession := baseSession
			defaultSession.AccessLevel = contracts.AccessLevelDefault
			defaultSession.AgentHasRuntimeSandbox = test.sandbox
			defaultPlan := ReviewBashCommand(config.AccessPolicyConfig{}, defaultSession, test.command, test.cwd, nil)
			if !defaultPlan.RequiresApproval() || !strings.HasPrefix(defaultPlan.RuleKey, "bash-access:opaque:") {
				t.Fatalf("default script execution must require opaque approval: %#v", defaultPlan)
			}

			autoSession := baseSession
			autoSession.AccessLevel = contracts.AccessLevelAutoApprove
			autoSession.AgentHasRuntimeSandbox = test.sandbox
			autoPlan := ReviewBashCommand(config.AccessPolicyConfig{}, autoSession, test.command, test.cwd, nil)
			if !autoPlan.AutoApproved() || !strings.HasPrefix(autoPlan.RuleKey, "bash-access:opaque:") {
				t.Fatalf("auto_approve script execution must be auto-approved: %#v", autoPlan)
			}
			if autoPlan.AccessLevel != contracts.AccessLevelAutoApprove {
				t.Fatalf("access level = %q, want auto_approve", autoPlan.AccessLevel)
			}
		})
	}
}

func TestTempRootReadonlyAndSymlinkEscapeRemainBlocked(t *testing.T) {
	workspace := t.TempDir()
	session := withSystemTempRoots(contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
	})
	readonlyCfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelDefault: {ReadonlyRoots: []string{"@temp"}},
	}}
	readonly, err := BuildPathPlan(readonlyCfg, session, WriteAccess, "@temp/readonly.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !readonly.Blocked() || !strings.Contains(readonly.Reason, "readonly") {
		t.Fatalf("explicit temporary readonly root should block writes: %#v", readonly)
	}

	primary, ok := temppaths.System().Primary()
	if !ok {
		t.Fatal("system temporary root is unavailable")
	}
	outside, err := filepath.Abs("accesspolicy_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if state, _, _, classifyErr := temppaths.System().Classify(outside); classifyErr != nil || state != temppaths.Outside {
		t.Skipf("test source is not outside system temporary roots: state=%s err=%v", state, classifyErr)
	}
	handle, err := os.CreateTemp(primary.Host, "agent-platform-temp-escape-*")
	if err != nil {
		t.Fatal(err)
	}
	link := handle.Name()
	_ = handle.Close()
	_ = os.Remove(link)
	t.Cleanup(func() { _ = os.Remove(link) })
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	escape, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, link)
	if err != nil {
		t.Fatal(err)
	}
	if !escape.Blocked() || !strings.Contains(escape.Reason, "escapes") {
		t.Fatalf("temporary symlink escape should be blocked: %#v", escape)
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
		AccessLevel:   contracts.AccessLevelDefault,
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
		AccessLevel:   contracts.AccessLevelDefault,
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
		AccessLevel:   contracts.AccessLevelDefault,
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

func TestSessionHostAccessRootsAllowOwnerReadWrite(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	owner := filepath.Join(root, "owner")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(owner, 0o755); err != nil {
		t.Fatalf("mkdir owner: %v", err)
	}
	session := contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: workspace,
				OwnerDir:     owner,
			},
		},
		RuntimeHostAccess: contracts.HostAccessRoots{
			ReadRoots:  []string{"@owner"},
			WriteRoots: []string{"@owner"},
		},
	}
	cfg := config.AccessPolicyConfig{}
	ownerFile := filepath.Join(owner, "OWNER.md")

	readPlan, err := BuildPathPlan(cfg, session, ReadAccess, ownerFile)
	if err != nil {
		t.Fatalf("build owner read plan: %v", err)
	}
	if !readPlan.Allowed() || readPlan.RequiresApproval() {
		t.Fatalf("expected owner read allowed by hostAccess, got %#v", readPlan)
	}
	writePlan, err := BuildPathPlan(cfg, session, WriteAccess, ownerFile)
	if err != nil {
		t.Fatalf("build owner write plan: %v", err)
	}
	if !writePlan.Allowed() || writePlan.RequiresApproval() {
		t.Fatalf("expected owner write allowed by hostAccess, got %#v", writePlan)
	}
	bashPlan := ReviewBashCommand(cfg, session, "touch "+ownerFile, workspace, nil)
	if !bashPlan.Allowed() || bashPlan.RequiresApproval() {
		t.Fatalf("expected owner bash write allowed by hostAccess, got %#v", bashPlan)
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

func TestBashAccessPolicyWorkspaceCwdAndPathDecisions(t *testing.T) {
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
				WorkspaceDir: workspace,
				ChatDir:      chatDir,
			},
		},
	}

	plan := ReviewBashCommand(config.AccessPolicyConfig{}, session, "touch "+filepath.Join(chatDir, "artifact.md"), workspace, nil)
	if !plan.Allowed() || plan.RequiresApproval() {
		t.Fatalf("expected chat bash write allowed, got %#v", plan)
	}
}

func TestBashAccessPolicyDevNullRedirectionIsNeutral(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	cfg := config.AccessPolicyConfig{}

	for _, command := range []string{
		"echo ok >/dev/null",
		"echo ok >>/dev/null",
		"echo ok &>/dev/null",
		"echo ok 2>/dev/null",
		"echo ok 2>&1",
		"echo ok 2>&-",
		"cat <&0",
		"cat <&-",
		"cat </dev/null",
		"cat <<< 'literal text'",
		"cat <<EOF\n" + outside + "\nEOF",
	} {
		session := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, WorkspaceRoot: workspace}
		plan := ReviewBashCommand(cfg, session, command, workspace, nil)
		if !plan.Allowed() || plan.RequiresApproval() {
			t.Fatalf("expected neutral redirection allowed for %q, got %#v", command, plan)
		}
	}

	readCommand := "find " + outside + ` -maxdepth 1 -name "*.md" -type f 2>/dev/null`
	defaultSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, WorkspaceRoot: workspace}
	defaultPlan := ReviewBashCommand(cfg, defaultSession, readCommand, workspace, nil)
	if !defaultPlan.RequiresApproval() || !strings.Contains(defaultPlan.RuleKey, "access-read") {
		t.Fatalf("expected default outside read approval, got %#v", defaultPlan)
	}
	if strings.Contains(defaultPlan.RuleKey, "access-write") {
		t.Fatalf("did not expect dev/null redirection to become write approval, got %#v", defaultPlan)
	}

	autoSession := contracts.QuerySession{AccessLevel: contracts.AccessLevelAutoApprove, WorkspaceRoot: workspace}
	autoPlan := ReviewBashCommand(cfg, autoSession, readCommand, workspace, nil)
	if !autoPlan.AutoApproved() || !strings.Contains(autoPlan.RuleKey, "access-read") {
		t.Fatalf("expected auto-approved outside read, got %#v", autoPlan)
	}

	writeCommand := "find " + outside + " -maxdepth 1 2>" + filepath.Join(outside, "err.log")
	writePlan := ReviewBashCommand(cfg, autoSession, writeCommand, workspace, nil)
	if !writePlan.RequiresApproval() || !strings.Contains(writePlan.RuleKey, "access-write") {
		t.Fatalf("expected real stderr file redirection to require write approval, got %#v", writePlan)
	}

	heredocWriteCommand := "cat <<EOF > " + filepath.Join(outside, "heredoc.log") + "\nhello\nEOF"
	heredocWritePlan := ReviewBashCommand(cfg, autoSession, heredocWriteCommand, workspace, nil)
	if !heredocWritePlan.RequiresApproval() || !strings.Contains(heredocWritePlan.RuleKey, "access-write") {
		t.Fatalf("expected heredoc output redirection to require write approval, got %#v", heredocWritePlan)
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
	npxOpaque := ReviewBashCommand(cfg, defaultSession, "npx tsc --noEmit", workspace, nil)
	if !npxOpaque.RequiresApproval() || !strings.Contains(npxOpaque.RuleKey, "bash-access:opaque") {
		t.Fatalf("expected npx opaque bash approval, got %#v", npxOpaque)
	}
	npxWithExitCode := ReviewBashCommand(cfg, defaultSession, `npx tsc --noEmit 2>&1; echo "Exit code: $?"`, workspace, nil)
	if !npxWithExitCode.RequiresApproval() || !strings.Contains(npxWithExitCode.RuleKey, "bash-access:opaque") || npxWithExitCode.RuleKey == "bash-access:complex" {
		t.Fatalf("expected npx command with exit code to use opaque approval, got %#v", npxWithExitCode)
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

func realPathForTest(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}
