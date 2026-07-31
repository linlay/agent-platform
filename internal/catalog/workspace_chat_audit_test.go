package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestAuditWorkspaceChatConfigReportsBlockingMigrationIssues(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	chatsDir := filepath.Join(root, "chats")
	workspace := filepath.Join(root, "workspace")
	source := filepath.Join(root, "source")
	for _, dir := range []string{agentsDir, chatsDir, workspace, source} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAuditAgent(t, agentsDir, "coder", "key: coder\nmode: CODER\nmodelConfig:\n  modelKey: mock\n")
	writeAuditAgent(t, agentsDir, "reserved", "key: reserved\nmode: REACT\nmodelConfig:\n  modelKey: mock\nruntimeConfig:\n  workspaceRoot: "+filepath.ToSlash(workspace)+"\n  sandboxMounts:\n    - source: "+filepath.ToSlash(source)+"\n      destination: /chat/cache\n      mode: rw\n")
	writeAuditAgent(t, agentsDir, "overlap", "key: overlap\nmode: REACT\nmodelConfig:\n  modelKey: mock\nruntimeConfig:\n  workspaceRoot: "+filepath.ToSlash(chatsDir)+"\n")
	writeAuditAgent(t, agentsDir, "kbase-dual", "key: kbase-dual\nmode: KBASE\nmodelConfig:\n  modelKey: mock\nruntimeConfig:\n  workspaceRoot: "+filepath.ToSlash(workspace)+"\nkbaseConfig:\n  source:\n    root: "+filepath.ToSlash(source)+"\n")

	findings, err := AuditWorkspaceChatConfig(config.Config{Paths: config.PathsConfig{
		AgentsDir: agentsDir,
		ChatsDir:  chatsDir,
	}})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	got := map[string]bool{}
	for _, finding := range findings {
		got[finding.AgentKey+":"+finding.Code] = true
	}
	for _, want := range []string{
		"coder:invalid_workspace_chat_config",
		"reserved:reserved_sandbox_mount",
		"overlap:workspace_chats_overlap",
		"kbase-dual:invalid_workspace_chat_config",
	} {
		if !got[want] {
			t.Fatalf("missing finding %q in %#v", want, findings)
		}
	}
}

func TestAuditWorkspaceChatConfigReportsMaskRequirementAndOrphanReferences(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	teamsDir := filepath.Join(root, "teams")
	chatsDir := filepath.Join(root, "runtime", "chats")
	for _, dir := range []string{agentsDir, teamsDir, chatsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAuditAgent(t, agentsDir, "root-agent", "key: root-agent\nmode: REACT\nmodelConfig:\n  modelKey: mock\nruntimeConfig:\n  environmentId: shell\n  workspaceRoot: "+filepath.ToSlash(root)+"\ncontextConfig:\n  agents:\n    - missing-agent\n")
	writeAuditAgent(t, agentsDir, "workspace-less", "key: workspace-less\nmode: REACT\nmodelConfig:\n  modelKey: mock\ntoolConfig:\n  tools:\n    - bash\n    - file_read\n    - file_glob\n")
	teamDir := filepath.Join(teamsDir, "demo")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "team.yml"), []byte("name: demo\nagentKeys:\n  - root-agent\n  - missing-member\norchestrator:\n  modelConfig:\n    modelKey: mock\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := AuditWorkspaceChatConfig(config.Config{Paths: config.PathsConfig{
		AgentsDir: agentsDir,
		TeamsDir:  teamsDir,
		ChatsDir:  chatsDir,
	}})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	got := map[string]WorkspaceChatAuditFinding{}
	for _, finding := range findings {
		got[finding.AgentKey+":"+finding.Code] = finding
	}
	if finding, ok := got["root-agent:container_workspace_mask_required"]; !ok || finding.Severity != "info" {
		t.Fatalf("missing informational mask finding: %#v", findings)
	}
	if finding, ok := got["workspace-less:workspace_less_explicit_paths_required"]; !ok ||
		finding.Severity != "info" ||
		!strings.Contains(finding.Message, "bash.cwd") ||
		!strings.Contains(finding.Message, "file_glob.path") {
		t.Fatalf("missing Workspace-less explicit path diagnostic: %#v", findings)
	}
	for _, key := range []string{
		"root-agent:orphan_agent_reference",
		"team/demo:orphan_agent_reference",
	} {
		if finding, ok := got[key]; !ok || finding.Severity != "error" {
			t.Fatalf("missing blocking orphan finding %q: %#v", key, findings)
		}
	}
}

func writeAuditAgent(t *testing.T, agentsDir string, key string, content string) {
	t.Helper()
	dir := filepath.Join(agentsDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
