package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func writeSkillScript(t *testing.T, parent, key string) string {
	t.Helper()
	writeTestSkill(t, parent, key)
	scripts := filepath.Join(parent, key, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(scripts, "task.sh")
	if err := os.WriteFile(p, []byte("echo skill\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSkillExecutionConfiguredAndSelectedOnly(t *testing.T) {
	runtimeDir := t.TempDir()
	center := t.TempDir()
	configured := writeSkillScript(t, filepath.Join(runtimeDir, "skills"), "configured")
	extra := writeSkillScript(t, center, "extra")
	unselected := writeSkillScript(t, center, "unselected")
	unconfigured := writeSkillScript(t, filepath.Join(runtimeDir, "skills"), "unconfigured")
	def := catalog.AgentDefinition{Key: "a", RuntimeDir: runtimeDir, Skills: []string{"configured"}}
	session := contracts.QuerySession{AgentKey: "a", RunID: "r"}
	selected := []resolvedMustUseSkill{{Key: "extra", RootPath: filepath.Dir(filepath.Dir(extra)), Extra: true}, {Key: "configured", RootPath: filepath.Dir(filepath.Dir(configured))}}
	session.SkillScripts = buildSkillScriptScope(session, def, selected)
	ctx := contracts.ExecutionContext{Session: session}
	for _, p := range []string{configured, extra} {
		if !session.SkillScripts.Matches(ctx.ScriptOwner(), p, "", false) {
			t.Fatal("missing selected grant: ", p)
		}
	}
	for _, p := range []string{unselected, unconfigured} {
		if session.SkillScripts.Matches(ctx.ScriptOwner(), p, "", false) {
			t.Fatal("unselected grant: ", p)
		}
	}
	if len(session.SkillScripts.Roots()) != 2 {
		t.Fatal("roots not deduplicated")
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SkillScripts") {
		t.Fatal("execution proof serialized")
	}
	var restored contracts.QuerySession
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SkillScripts != nil {
		t.Fatal("execution proof restored")
	}
}
func TestBuildQuerySessionSkillExecutionAndRecovery(t *testing.T) {
	runtimeDir := t.TempDir()
	file := writeSkillScript(t, filepath.Join(runtimeDir, "skills"), "configured")
	def := catalog.AgentDefinition{Key: "a", Mode: "REACT", RuntimeDir: runtimeDir, Skills: []string{"configured"}}
	s := &Server{}
	req := api.QueryRequest{AgentKey: "a", RunID: "r", ChatID: "c", Role: "user"}
	session, err := s.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "c"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contracts.ExecutionContext{Session: session}
	if !session.SkillScripts.Matches(ctx.ScriptOwner(), file, "", false) {
		t.Fatal("configured skill was not granted")
	}
	restored, err := s.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "c"}, def, querySessionBuildOptions{DisableSkillScriptGrants: true})
	if err != nil {
		t.Fatal(err)
	}
	if restored.SkillScripts != nil {
		t.Fatal("recovery rebuilt proof")
	}
}
