package session

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"context"
	"os"
	"reflect"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type promptProfilesFunc func(runtimetypes.QueryCommand, contracts.QuerySession) ([]contracts.SystemInitProfile, error)

func (f promptProfilesFunc) Profiles(req runtimetypes.QueryCommand, session contracts.QuerySession) ([]contracts.SystemInitProfile, error) {
	return f(req, session)
}

func TestRunPromptSnapshotSurvivesRestartAndLocaleChange(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{StateDir: t.TempDir()}}
	calls := 0
	builder := New(Dependencies{Config: cfg, Profiles: promptProfilesFunc(func(_ runtimetypes.QueryCommand, session contracts.QuerySession) ([]contracts.SystemInitProfile, error) {
		calls++
		return []contracts.SystemInitProfile{
			{AgentKey: "coder", CacheKey: "coder:plan", Initial: true, SystemMessage: map[string]any{"role": "system", "content": "plan " + session.Locale}},
			{AgentKey: "coder", CacheKey: "coder:execute", SystemMessage: map[string]any{"role": "system", "content": "execute " + session.Locale}},
		}, nil
	})})
	first := contracts.QuerySession{RunID: "run-1", ChatID: "chat", Locale: "en", EnvironmentPromptTemplate: "original {{locale}}"}
	profiles, err := builder.frozenSystemProfiles(runtimetypes.QueryCommand{}, &first)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Prompts.Runtime.DefaultLocale = "zh-CN"
	restored := New(Dependencies{Config: cfg, Profiles: promptProfilesFunc(func(runtimetypes.QueryCommand, contracts.QuerySession) ([]contracts.SystemInitProfile, error) {
		t.Fatal("resumed Run must not render profiles again")
		return nil, nil
	})})
	for _, locale := range []string{"zh-CN", ""} {
		session, err := restored.BuildQuerySession(context.Background(), runtimetypes.QueryCommand{RunID: "run-1", ChatID: "chat", AgentKey: "coder"}, chat.Summary{ChatID: "chat"}, catalog.AgentDefinition{Key: "coder", Mode: "GENERAL"}, Options{Locale: locale, DisableSkillScriptGrants: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := restored.frozenSystemProfiles(runtimetypes.QueryCommand{}, &session)
		if err != nil {
			t.Fatal(err)
		}
		if session.Locale != "en" || session.EnvironmentPromptTemplate != first.EnvironmentPromptTemplate || !reflect.DeepEqual(got, profiles) {
			t.Fatalf("changed restored context: %+v %+v", session, got)
		}
	}
	if locale, err := restored.RunPromptLocale("run-1"); err != nil || locale != "en" {
		t.Fatalf("child Run inheritance: %q %v", locale, err)
	}
	next := contracts.QuerySession{RunID: "run-2", ChatID: "chat", Locale: "zh-CN"}
	if _, err := builder.frozenSystemProfiles(runtimetypes.QueryCommand{}, &next); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("new Run did not render: %d", calls)
	}
	child := first
	child.SubTaskID = "child-1"
	if _, err := builder.frozenSystemProfiles(runtimetypes.QueryCommand{}, &child); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("child collided with parent")
	}
	if err := os.WriteFile(builder.promptSnapshotPath("run-1", ""), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.frozenSystemProfiles(runtimetypes.QueryCommand{}, &first); err == nil {
		t.Fatal("corrupt state silently rebuilt")
	}
}
