package shellreview

import (
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
	"strings"
	"testing"
)

func hasNullWriteRequirement(p shellplan.BashPlan) bool {
	if len(p.Requirements) > 0 {
		for _, leaf := range p.Requirements {
			if hasNullWriteRequirement(leaf) {
				return true
			}
		}
		return false
	}
	return strings.HasPrefix(p.RuleKey, "bash-access:path:access-write") && p.Scope == "/dev/null"
}
func guestNullEnvironment(probe func() (string, error)) *types.BashEnvironment {
	return &types.BashEnvironment{InspectNullDevice: probe, Resolve: func(name, cwd string, vars map[string]string) (string, error) {
		if strings.HasPrefix(name, "/") {
			return name, nil
		}
		return "/usr/bin/" + name, nil
	}}
}
func TestNullResourceOffsetsStayLocalToInvocationAndSource(t *testing.T) {
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove} {
		f := newCapabilityFixture(t, level)
		f.session.AgentHasRuntimeSandbox = true
		f.session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
		for _, tc := range []struct {
			command string
			path    bool
			probes  int
		}{
			{"cat /dev/null", false, 1}, {"tee -a /dev/null", false, 1},
			{"unregistered 2>/dev/null", false, 1}, {"ls --unknown 2>/dev/null", false, 1},
			{"ls --unknown; tee /dev/null", false, 1},
			{`sink=/dev/null; tee "$sink"`, true, 0}, {`sink=/dev/null; unregistered 2>"$sink"`, true, 0},
			{"tee --unknown /dev/null", true, 0}, {"unregistered /dev/null", true, 0},
			{"env -S 'tee /dev/null'", true, 0}, {"env -S tee /dev/null", false, 1},
			{"tee //dev/null", true, 0}, {"tee /dev/./null", true, 0},
			{"cp notes /dev/null", true, 0}, {"mv notes /dev/null", true, 0}, {"ln notes /dev/null", true, 0}, {"chmod 600 /dev/null", true, 0}, {"rm /dev/null", true, 0},
			{"printf ok >& /dev/null", false, 1}, {"printf ok 2>&1", false, 0},
		} {
			calls := 0
			env := guestNullEnvironment(func() (string, error) { calls++; return "21b6|1|3", nil })
			p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, tc.command, "/workspace", map[string]string{"PATH": "/usr/bin"}, env, nil)
			if hasNullWriteRequirement(p) != tc.path || calls != tc.probes {
				t.Fatalf("%s at %s: null write=%t probes=%d want=%t/%d: %+v", tc.command, level, hasNullWriteRequirement(p), calls, tc.path, tc.probes, p)
			}
		}
	}
}
func TestNullReadonlyAndAdminBlocksRunBeforeDeviceProbe(t *testing.T) {
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		f.session.AgentHasRuntimeSandbox = true
		f.session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
		cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{level: {ReadonlyRoots: []string{"/dev/null"}}}}
		for _, command := range []string{"tee /dev/null", "unregistered 2>/dev/null", "printf ok >/dev/null"} {
			env := guestNullEnvironment(func() (string, error) { t.Fatal("readonly was probed for an offset"); return "", nil })
			if p := ReviewBashCommandInEnvironment(cfg, f.session, command, "/workspace", nil, env, nil); !p.Blocked() {
				t.Fatalf("%s at %s: %+v", command, level, p)
			}
		}
	}
}
func TestDeviceProbeIsFreshBetweenReviews(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	f.session.AgentHasRuntimeSandbox = true
	f.session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	calls := 0
	env := guestNullEnvironment(func() (string, error) {
		calls++
		if calls == 1 {
			return "21b6|1|3", nil
		}
		return "81b6|0|0", nil
	})
	command := "cat /dev/null; tee /dev/null; unregistered 2>/dev/null"
	first := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, "/workspace", nil, env, nil)
	if calls != 1 || hasNullWriteRequirement(first) {
		t.Fatalf("one-review device cache: calls=%d %+v", calls, first)
	}
	second := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, "/workspace", nil, env, nil)
	if calls != 2 || !hasNullWriteRequirement(second) || !second.RequiresApproval() {
		t.Fatalf("new review reused stale device identity: calls=%d %+v", calls, second)
	}
}
