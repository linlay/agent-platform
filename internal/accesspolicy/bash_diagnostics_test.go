package accesspolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestDiagnosticCommandsAccess(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		name := "host"
		if sandbox {
			name = "sandbox"
		}
		t.Run(name, func(t *testing.T) {
			if !sandbox && runtime.GOOS == "windows" {
				t.Skip("Unix diagnostic commands and /dev/null")
			}
			f := newCapabilityFixture(t, AccessLevelAskForApproval)
			f.session.AgentHasRuntimeSandbox = sandbox
			f.session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
			var env *BashEnvironment
			cwd := f.workspace
			if sandbox {
				cwd = "/workspace"
				env = &BashEnvironment{Resolve: func(name, cwd string, vars map[string]string) (string, error) {
					if strings.HasPrefix(name, "/") {
						return name, nil
					}
					return "/usr/bin/" + name, nil
				}}
			}
			for _, tc := range []struct {
				command string
				allow   bool
			}{
				{`lsof -nP -iTCP:8080 -iTCP:8081 -iTCP:8082 -sTCP:LISTEN`, true},
				{`lsof -nP -i :8080`, true},
				{`lsof -a -p 123 -iTCP -sTCP:LISTEN`, true},
				{`lsof -D`, false},
				{`lsof -Dbcache`, false},
				{`lsof --unknown`, false},
				{`lsof +D /outside`, false},
				{`lsof /outside/file`, false},
				{`curl -s -o /dev/null -w "admin-api %{http_code}\n" http://127.0.0.1:8081/healthz; curl -s -o /dev/null -w "web %{http_code}\n" http://127.0.0.1:8082/; curl -s -o /dev/null -w "mcp %{http_code}\n" http://127.0.0.1:8083/mcp`, true},
				{`curl -s --output=/dev/null http://127.0.0.1:8081/healthz`, true},
				{`wget -q -O /dev/null http://127.0.0.1:8081/healthz`, true},
				{`curl -s -o /outside/file http://127.0.0.1:8081/healthz`, false},
				{`curl -s -o /dev/null -X DELETE http://127.0.0.1:8081/resource`, false},
				{`curl -s -o /dev/null -d x=1 http://127.0.0.1:8081/resource`, false},
				{`curl -s -o /dev/null -T /outside/input http://127.0.0.1:8081/resource`, false},
				{`curl -s -o /dev/null http://127.0.0.1:8081/ > /outside/file`, false},
				{`rm /dev/null`, false},
			} {
				t.Run(tc.command, func(t *testing.T) {
					if !sandbox {
						if _, err := exec.LookPath(strings.Fields(tc.command)[0]); err != nil {
							t.Skip(err)
						}
					}
					plan := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, tc.command, cwd, nil, env, nil)
					if plan.Allowed() != tc.allow {
						t.Fatalf("allow=%v: %+v", tc.allow, plan)
					}
				})
			}
			// A workspace program with the same name never inherits system lsof's identity.
			if err := os.WriteFile(filepath.Join(f.workspace, "lsof"), []byte("#!/bin/sh\necho shadow\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			plan := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, filepath.ToSlash(filepath.Join(cwd, "lsof"))+" -nP -iTCP:8080", cwd, nil, env, nil)
			if plan.Allowed() {
				t.Fatalf("workspace lsof must remain opaque: %+v", plan)
			}
			if !sandbox {
				t.Setenv("PATH", f.workspace+string(os.PathListSeparator)+os.Getenv("PATH"))
				if plan := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, "lsof -nP -iTCP:8080", cwd, nil); plan.Allowed() {
					t.Fatalf("PATH shadow must remain opaque: %+v", plan)
				}
			}
			cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
				AccessLevelAskForApproval: {ReadonlyRoots: []string{"/dev/null"}},
			}}
			plan = ReviewBashCommandInEnvironment(cfg, f.session, "curl -s -o /dev/null http://127.0.0.1:8081/healthz", cwd, nil, env, nil)
			if !plan.Blocked() {
				t.Fatalf("null output must retain hard readonly protection: %+v", plan)
			}
		})
	}
}
