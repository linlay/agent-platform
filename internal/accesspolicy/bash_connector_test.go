package accesspolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

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

func TestMountedConnectorAllOperationsAndAccessLevels(t *testing.T) {
	session, vars, bin := connectorFixture(t)
	commands := []string{
		"wecom-cli auth show --status",
		"wecom-cli message aibot sessions list",
		"wecom-cli message aibot send --json '{\n\"content\":\"中午12点会议 /proc/self/environ rm -rf /　\"\n}'",
		"wecom-cli media upload --file /outside/private/file.pdf --output /outside/result",
		"'" + filepath.Join(bin, "wecom-cli") + "' send",
		"sh '" + filepath.Join(bin, "wecom-cli") + "' send",
		"node '" + filepath.Join(bin, "launcher.cjs") + "' send",
		"wecom-cli.cmd send",
		"dbx execute --sql 'DROP TABLE demo'",
		"env -C / wecom-cli send",
		"wecom-cli auth show --status && wecom-cli message aibot sessions list",
	}
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		session.AccessLevel = level
		for _, command := range commands {
			t.Run(level+"/"+command, func(t *testing.T) {
				p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, "/", vars)
				if p.Decision != DecisionAllow || !p.ConnectorOnly {
					t.Fatalf("not direct allow: %+v", p)
				}
				if s := p.SecurityReview(command, vars); s.Decision != bashsec.ReviewAllow {
					t.Fatalf("security still intercepted: %+v / %s", s, p.ReviewCommand)
				}
			})
		}
	}
}

func TestMountedConnectorLeavesSurroundingShellUnderReview(t *testing.T) {
	session, vars, _ := connectorFixture(t)
	for _, command := range []string{
		"wecom-cli send && python3 -c 'print(1)'",
		"wecom-cli send > /outside/result",
		"wecom-cli send \"$(python3 -c 'print(1)')\"",
		"wecom-cli send | cat /outside/file",
	} {
		p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, "", vars)
		if !p.RequiresApproval() || !p.HasConnector || p.ConnectorOnly {
			t.Fatalf("lost shell review for %s: %+v", command, p)
		}
		if strings.Contains(p.ReviewCommand, "wecom-cli") {
			t.Fatalf("CLI remained in projection: %s", p.ReviewCommand)
		}
	}
	for _, command := range []string{"wecom-cli send; eval 'echo unsafe'", "wecom-cli send < /outside/file", "wecom-cli send \"$(eval 'echo unsafe')\""} {
		p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, "", vars)
		if s := p.SecurityReview(command, vars); s.Decision != bashsec.ReviewBlock {
			t.Fatalf("lost shell block for %s: %+v", command, s)
		}
	}
}

func TestMountedConnectorIdentityCannotBeReused(t *testing.T) {
	session, vars, bin := connectorFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "wecom-cli"), []byte("#!/bin/sh\nprintf outside"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"PATH='" + outside + "' wecom-cli send",
		"env PATH='" + outside + "' wecom-cli send",
		"'" + filepath.Join(outside, "wecom-cli") + "' send",
		"node -e 'console.log(1)' '" + filepath.Join(bin, "launcher.cjs") + "'",
		"cd '" + outside + "' && ./wecom-cli send",
	} {
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, command, "", vars); p.HasConnector {
			t.Fatalf("wrong target trusted: %s %+v", command, p)
		}
	}
	other := session
	other.ConnectorDirs = nil
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, other, "wecom-cli send", "", vars); p.HasConnector {
		t.Fatal("grant leaked to unmounted Agent")
	}
	if err := os.WriteFile(filepath.Join(bin, "wecom-cli"), []byte("#!/bin/sh\nprintf changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, session, "wecom-cli send", "", vars); p.HasConnector {
		t.Fatal("changed content reused old grant")
	}
}

func TestMountedConnectorContainerUsesGuestIdentityAndHash(t *testing.T) {
	session, vars, _ := connectorFixture(t)
	session.AgentHasRuntimeSandbox = true
	session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	guest := "/connectors/wecom/bin/wecom-cli"
	hash := ""
	for _, entry := range session.ConnectorCLIEntries {
		if entry.RelativePath == "bin/wecom-cli" {
			hash = entry.SHA256
		}
	}
	canonical := guest
	env := &BashEnvironment{
		Directory: func(string) (string, error) { return "/workspace", nil },
		Resolve: func(name, cwd string, vars map[string]string) (string, error) {
			if name == "wecom-cli" {
				return canonical, nil
			}
			return "", fmt.Errorf("missing")
		},
		Canonical: func(p, cwd string) (string, error) { return canonical, nil },
		Inspect:   func(string) (string, string, error) { return "#!/bin/sh\n", hash, nil },
	}
	review := func() BashPlan {
		return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, "wecom-cli send", "", vars, env, nil)
	}
	if p := review(); !p.ConnectorOnly || p.Decision != DecisionAllow {
		t.Fatalf("guest entry not allowed: %+v", p)
	}
	canonical = "/outside/wecom-cli"
	if p := review(); p.HasConnector {
		t.Fatal("guest symlink escape trusted")
	}
	canonical = guest
	hash = "changed"
	if p := review(); p.HasConnector {
		t.Fatal("guest content mismatch trusted")
	}
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, "wecom-cli send", "", vars, nil, nil); p.HasConnector {
		t.Fatal("host PATH substituted for guest resolution")
	}
}
