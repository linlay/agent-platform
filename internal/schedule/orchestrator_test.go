package schedule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
)

type fakeTeamLookup struct {
	teams map[string]catalog.TeamDefinition
}

func (f fakeTeamLookup) TeamDefinition(teamID string) (catalog.TeamDefinition, bool) {
	def, ok := f.teams[teamID]
	return def, ok
}

func TestRegistryLoadsStructuredScheduleDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daily.demo.yml"), []byte(
		"name: Daily Demo\n"+
			"description: Demo schedule\n"+
			"enabled: true\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: demo-agent\n"+
			"environment:\n"+
			"  zoneId: Asia/Shanghai\n"+
			"query:\n"+
			"  requestId: req_daily_001\n"+
			"  chatId: 123e4567-e89b-12d3-a456-426614174000\n"+
			"  role: system\n"+
			"  message: hello\n"+
			"  hidden: true\n"+
			"  params:\n"+
			"    source: schedule\n"+
			"  scene:\n"+
			"    url: https://example.com/app\n"+
			"    title: demo\n"+
			"  references:\n"+
			"    - id: ref_001\n"+
			"      type: url\n"+
			"      name: doc\n"+
			"      url: https://example.com/doc\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}

	defs, err := NewRegistry(root, nil).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected one schedule, got %#v", defs)
	}
	def := defs[0]
	if def.ID != "daily" || def.Name != "Daily Demo" || def.Description != "Demo schedule" {
		t.Fatalf("unexpected definition header %#v", def)
	}
	if !def.Enabled || def.Cron != "*/1 * * * * *" || def.AgentKey != "demo-agent" {
		t.Fatalf("unexpected definition fields %#v", def)
	}
	if def.Environment.ZoneID != "Asia/Shanghai" {
		t.Fatalf("expected zone id, got %#v", def.Environment)
	}
	if def.Query.RequestID != "req_daily_001" || def.Query.ChatID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("unexpected query ids %#v", def.Query)
	}
	if def.Query.Role != "system" || def.Query.Message != "hello" {
		t.Fatalf("unexpected query core fields %#v", def.Query)
	}
	if def.Query.Hidden == nil || !*def.Query.Hidden {
		t.Fatalf("expected hidden=true, got %#v", def.Query.Hidden)
	}
	if len(def.Query.References) != 1 || def.Query.References[0].URL != "https://example.com/doc" {
		t.Fatalf("unexpected references %#v", def.Query.References)
	}
	if def.Query.Scene == nil || def.Query.Scene.Title != "demo" {
		t.Fatalf("unexpected scene %#v", def.Query.Scene)
	}
	if def.Query.Params["source"] != "schedule" {
		t.Fatalf("unexpected params %#v", def.Query.Params)
	}
}

func TestRegistrySkipsExampleScheduleDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"name: Demo\n"+
			"description: valid\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: demo-agent\n"+
			"query:\n"+
			"  message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.example.yml"), []byte(
		"name: Ignored\n"+
			"description: ignored\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: ignored-agent\n"+
			"query:\n"+
			"  message: ignored\n",
	), 0o644); err != nil {
		t.Fatalf("write example schedule file: %v", err)
	}

	defs, err := NewRegistry(root, nil).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "demo" {
		t.Fatalf("expected one loadable schedule, got %#v", defs)
	}
}

func TestRegistrySkipsInvalidSchedules(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"missing-message.yml": "name: Missing Message\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nquery:\n  hidden: true\n",
		"invalid-chat.yml":    "name: Invalid Chat\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nquery:\n  message: hi\n  chatId: not-a-uuid\n",
		"invalid-zone.yml":    "name: Invalid Zone\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nenvironment:\n  zoneId: Mars/Base\nquery:\n  message: hi\n",
		"invalid-cron.yml":    "name: Invalid Cron\ndescription: bad\ncron: \"nope\"\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"valid.yml":           "name: Valid\ndescription: ok\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nquery:\n  message: hi\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	defs, err := NewRegistry(root, nil).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "valid" {
		t.Fatalf("expected only valid schedule to load, got %#v", defs)
	}
}

func TestRegistryValidatesTeamScopedSchedule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "valid.yml"), []byte(
		"name: Team Valid\n"+
			"description: ok\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: demo-agent\n"+
			"teamId: team-a\n"+
			"query:\n"+
			"  message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write valid schedule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "invalid.yml"), []byte(
		"name: Team Invalid\n"+
			"description: bad\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: other-agent\n"+
			"teamId: team-a\n"+
			"query:\n"+
			"  message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write invalid schedule: %v", err)
	}

	teams := fakeTeamLookup{teams: map[string]catalog.TeamDefinition{
		"team-a": {TeamID: "team-a", AgentKeys: []string{"demo-agent"}},
	}}
	defs, err := NewRegistry(root, teams).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "valid" {
		t.Fatalf("expected only valid team-scoped schedule, got %#v", defs)
	}
}

func TestOrchestratorDispatchesEnabledCronSchedule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"name: Demo\n"+
			"description: valid\n"+
			"enabled: true\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: demo-agent\n"+
			"query:\n"+
			"  message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "disabled.yml"), []byte(
		"name: Disabled\n"+
			"description: disabled\n"+
			"enabled: false\n"+
			"cron: \"*/1 * * * * *\"\n"+
			"agentKey: demo-agent\n"+
			"query:\n"+
			"  message: skipped\n",
	), 0o644); err != nil {
		t.Fatalf("write disabled schedule file: %v", err)
	}

	dispatched := make(chan api.QueryRequest, 2)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		dispatched <- req
		return nil
	}))
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer orchestrator.Stop()

	select {
	case req := <-dispatched:
		if req.Message != "hello" {
			t.Fatalf("unexpected dispatched request %#v", req)
		}
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("timed out waiting for scheduled dispatch")
	}
}
