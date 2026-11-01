package shellreview

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/skillsexec"
)

func authoredFixture(t *testing.T) (*ExecutionContext, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "task with space.sh")
	data := []byte("#!/bin/sh\nprintf 'ok\\n'\n")
	if err := os.WriteFile(p, data, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := &ExecutionContext{Session: QuerySession{AgentKey: "ordinary", RunID: "run-1", WorkspaceRoot: dir}}
	ctx.EnsureAuthoredScripts()
	ctx.AuthoredScripts.Record(ctx.ScriptOwner(), p, data, "", true)
	return ctx, p
}

// fakeSystemPrograms makes names resolve, through a private PATH, to recorded
// system installations, so cases do not depend on what the host has installed.
func fakeSystemPrograms(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("fake\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := pathutil.Canonicalize(p)
		if err != nil {
			t.Fatal(err)
		}
		old, had := systemExecutables[name]
		systemExecutables[name] = c.Key
		t.Cleanup(func() {
			if had {
				systemExecutables[name] = old
			} else {
				delete(systemExecutables, name)
			}
		})
	}
	return map[string]string{"PATH": dir}
}

func connectorFixture(t *testing.T) (QuerySession, map[string]string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"wecom-cli": "#!/bin/sh\nprintf ok\n", "launcher.cjs": "console.log('ok')\n", "wecom-cli.cmd": "@echo off\r\necho ok\r\n", "dbx": "binary fixture\x00"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := connector.SnapshotCLIEntries("wecom", root)
	if err != nil {
		t.Fatal(err)
	}
	session := QuerySession{AgentKey: "mounted", WorkspaceRoot: t.TempDir(), ConnectorDirs: map[string]string{"wecom": root}, ConnectorBinDirs: []string{bin}, ConnectorCLIEntries: entries}
	return session, map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}, bin
}

func skillFixture(t *testing.T) (*ExecutionContext, string) {
	t.Helper()
	ctx, _ := authoredFixture(t)
	ctx.AuthoredScripts = nil
	root := filepath.Join(ctx.Session.WorkspaceRoot, "selected")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "scripts", "task.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho skill\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root}})
	return ctx, file
}

type capabilityFixture struct {
	session   QuerySession
	workspace string
	chat      string
	temp      string
}

func newCapabilityFixture(t *testing.T, level string) capabilityFixture {
	t.Helper()
	base := t.TempDir()
	f := capabilityFixture{workspace: filepath.Join(base, "ws"), chat: filepath.Join(base, "chats", "c1"), temp: filepath.Join(base, "tmp")}
	for _, dir := range []string{f.workspace, f.chat, f.temp, filepath.Join(base, "agent"), filepath.Join(base, "agent", "skills")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.session = QuerySession{AccessLevel: level, WorkspaceRoot: f.workspace, ChatID: "c1", RunID: "r1", AgentKey: "a", TempRoot: f.temp, TempRoots: []string{f.temp}}
	f.session.RuntimeContext.LocalPaths.ChatsDir = filepath.Dir(f.chat)
	f.session.RuntimeContext.LocalPaths.ChatDir = f.chat
	f.session.RuntimeContext.LocalPaths.AgentDir = filepath.Join(base, "agent")
	f.session.RuntimeContext.LocalPaths.SkillsDir = filepath.Join(base, "agent", "skills")
	return f
}

func review(f capabilityFixture, command string, ctx ...*ExecutionContext) BashPlan {
	return ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, "", nil, ctx...)
}

func policyFixture(t *testing.T) QuerySession {
	t.Helper()
	root := t.TempDir()
	s := QuerySession{AccessLevel: AccessLevelAskForApproval, WorkspaceRoot: filepath.Join(root, "workspace"), TempRoot: filepath.Join(root, "temp")}
	s.TempRoots = []string{s.TempRoot}
	for _, dir := range []string{s.WorkspaceRoot, s.TempRoot} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
