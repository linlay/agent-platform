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

	hookDirs, env := resolveSkillRuntimeSettings([]string{"alpha", "beta", "alpha"}, registry)
	if !reflect.DeepEqual(hookDirs, []string{"/skills/alpha/.bash-hooks", "/skills/beta/.bash-hooks"}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	wantEnv := map[string]string{
		"NODE_ENV": "production",
		"DEBUG":    "1",
		"TZ":       "UTC",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
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
