package server

import (
	"context"
	"reflect"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
)

type skillRuntimeRegistry struct {
	testCatalogRegistry
	skills map[string]catalog.SkillDefinition
}

func (r skillRuntimeRegistry) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	def, ok := r.skills[key]
	return def, ok
}

func TestResolveSkillRuntimeSettingsMergesEnvAndHookDirsInOrder(t *testing.T) {
	registry := skillRuntimeRegistry{
		skills: map[string]catalog.SkillDefinition{
			"alpha": {
				Key:          "alpha",
				BashHooksDir: "/skills/alpha/.bash-hooks",
				SandboxEnv: map[string]string{
					"NODE_ENV": "development",
					"DEBUG":    "1",
				},
			},
			"beta": {
				Key:          "beta",
				BashHooksDir: "/skills/beta/.bash-hooks",
				SandboxEnv: map[string]string{
					"NODE_ENV": "production",
					"TZ":       "UTC",
				},
			},
		},
	}

	agentEnv := map[string]string{
		"NODE_ENV": "test",
		"BASE":     "1",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, []string{"alpha", "beta", "alpha"}, registry)
	if !reflect.DeepEqual(hookDirs, []string{"/skills/alpha/.bash-hooks", "/skills/beta/.bash-hooks"}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	wantEnv := map[string]string{
		"NODE_ENV": "production",
		"BASE":     "1",
		"DEBUG":    "1",
		"TZ":       "UTC",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
	}
}

func TestResolveSkillRuntimeSettingsSkipsMissingSkills(t *testing.T) {
	registry := skillRuntimeRegistry{
		skills: map[string]catalog.SkillDefinition{
			"beta": {
				Key:          "beta",
				BashHooksDir: "/skills/beta/.bash-hooks",
				SandboxEnv: map[string]string{
					"TZ": "UTC",
				},
			},
		},
	}

	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, []string{"missing", "beta"}, registry)
	if !reflect.DeepEqual(hookDirs, []string{"/skills/beta/.bash-hooks"}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	if !reflect.DeepEqual(env, map[string]string{"HTTP_PROXY": "http://agent", "TZ": "UTC"}) {
		t.Fatalf("env = %#v", env)
	}
}

func TestResolveSkillRuntimeSettingsReturnsAgentEnvWithoutSkills(t *testing.T) {
	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}

	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, nil, nil)
	if hookDirs != nil {
		t.Fatalf("hookDirs = %#v, want nil", hookDirs)
	}
	if !reflect.DeepEqual(env, agentEnv) {
		t.Fatalf("env = %#v, want %#v", env, agentEnv)
	}
	if env["HTTP_PROXY"] != "http://agent" {
		t.Fatalf("expected cloned env to preserve values, got %#v", env)
	}
}

func (skillRuntimeRegistry) Agents(string) []api.AgentSummary       { return nil }
func (skillRuntimeRegistry) Teams() []api.TeamSummary               { return nil }
func (skillRuntimeRegistry) Skills(string) []api.SkillSummary       { return nil }
func (skillRuntimeRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (skillRuntimeRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (skillRuntimeRegistry) DefaultAgentKey() string { return "" }
func (skillRuntimeRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{}, false
}
func (skillRuntimeRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (skillRuntimeRegistry) Reload(context.Context, string) error { return nil }
