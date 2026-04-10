package schedule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
			"remainingRuns: 3\n"+
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
	if def.RemainingRuns == nil || *def.RemainingRuns != 3 {
		t.Fatalf("expected remainingRuns=3, got %#v", def.RemainingRuns)
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
		"missing-message.yml":       "name: Missing Message\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nquery:\n  hidden: true\n",
		"invalid-chat.yml":          "name: Invalid Chat\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nquery:\n  message: hi\n  chatId: not-a-uuid\n",
		"invalid-zone.yml":          "name: Invalid Zone\ndescription: bad\ncron: \"*/1 * * * * *\"\nagentKey: demo-agent\nenvironment:\n  zoneId: Mars/Base\nquery:\n  message: hi\n",
		"invalid-cron.yml":          "name: Invalid Cron\ndescription: bad\ncron: \"nope\"\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-zero-runs.yml":     "name: Invalid Runs Zero\ndescription: bad\ncron: \"*/1 * * * * *\"\nremainingRuns: 0\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-negative-runs.yml": "name: Invalid Runs Negative\ndescription: bad\ncron: \"*/1 * * * * *\"\nremainingRuns: -1\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-text-runs.yml":     "name: Invalid Runs Text\ndescription: bad\ncron: \"*/1 * * * * *\"\nremainingRuns: nope\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"valid.yml":                 "name: Valid\ndescription: ok\ncron: \"*/1 * * * * *\"\nremainingRuns: 2\nagentKey: demo-agent\nquery:\n  message: hi\n",
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
	if defs[0].RemainingRuns == nil || *defs[0].RemainingRuns != 2 {
		t.Fatalf("expected remainingRuns=2, got %#v", defs[0].RemainingRuns)
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
	defer waitForStop(t, orchestrator)

	req := waitForRequest(t, dispatched, 2500*time.Millisecond, func(req api.QueryRequest) bool {
		return req.Message == "hello"
	})
	if req.Message != "hello" {
		t.Fatalf("unexpected dispatched request %#v", req)
	}
}

func TestOrchestratorConsumesRemainingRunsAndDeletesFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "limited.yml")
	writeSchedule(t, path, scheduleBody("hello", "*/1 * * * * *", "remainingRuns: 2\n"))

	dispatched := make(chan api.QueryRequest, 4)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		dispatched <- req
		return nil
	}))
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	waitForRequest(t, dispatched, 2500*time.Millisecond, func(req api.QueryRequest) bool {
		return req.Message == "hello"
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schedule after first run: %v", err)
	}
	if !strings.Contains(string(data), "remainingRuns: 1") {
		t.Fatalf("expected remainingRuns to be decremented, got:\n%s", string(data))
	}

	waitForRequest(t, dispatched, 2500*time.Millisecond, func(req api.QueryRequest) bool {
		return req.Message == "hello"
	})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected schedule file deleted, got err=%v", err)
	}
	assertNoRequest(t, dispatched, 1500*time.Millisecond)
}

func TestOrchestratorConsumesRunOnDispatchFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "failing.yml")
	writeSchedule(t, path, scheduleBody("boom", "*/1 * * * * *", "remainingRuns: 1\n"))

	attempts := make(chan api.QueryRequest, 2)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		attempts <- req
		return context.DeadlineExceeded
	}))
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	waitForRequest(t, attempts, 2500*time.Millisecond, func(req api.QueryRequest) bool {
		return req.Message == "boom"
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected schedule file deleted after failed dispatch, got err=%v", err)
	}
	assertNoRequest(t, attempts, 1500*time.Millisecond)
}

func TestOrchestratorWatchesScheduleDirectory(t *testing.T) {
	root := t.TempDir()
	dispatched := make(chan api.QueryRequest, 8)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		dispatched <- req
		return nil
	}))
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	original := filepath.Join(root, "demo.yml")
	writeSchedule(t, original, scheduleBody("first", "*/2 * * * * *", ""))

	waitForRequest(t, dispatched, 5*time.Second, func(req api.QueryRequest) bool {
		return req.Message == "first"
	})

	writeSchedule(t, original, scheduleBody("second", "*/2 * * * * *", ""))
	waitForRequest(t, dispatched, 5*time.Second, func(req api.QueryRequest) bool {
		return req.Message == "second"
	})

	renamed := filepath.Join(root, "renamed.yml")
	if err := os.Rename(original, renamed); err != nil {
		t.Fatalf("rename schedule: %v", err)
	}
	req := waitForRequest(t, dispatched, 5*time.Second, func(req api.QueryRequest) bool {
		return req.Message == "second" && scheduleID(req) == "renamed"
	})
	if scheduleID(req) != "renamed" {
		t.Fatalf("expected renamed schedule id, got %#v", req.Params["__schedule"])
	}

	if err := os.Remove(renamed); err != nil {
		t.Fatalf("remove schedule: %v", err)
	}
	assertNoRequest(t, dispatched, 2500*time.Millisecond)
}

func waitForStop(t *testing.T, orchestrator *Orchestrator) {
	t.Helper()
	done := orchestrator.Stop()
	select {
	case <-done.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for orchestrator stop")
	}
}

func waitForRequest(t *testing.T, ch <-chan api.QueryRequest, timeout time.Duration, match func(api.QueryRequest) bool) api.QueryRequest {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case req := <-ch:
			if match == nil || match(req) {
				return req
			}
		case <-deadline:
			t.Fatal("timed out waiting for scheduled dispatch")
		}
	}
}

func assertNoRequest(t *testing.T, ch <-chan api.QueryRequest, timeout time.Duration) {
	t.Helper()
	select {
	case req := <-ch:
		t.Fatalf("expected no dispatch, got %#v", req)
	case <-time.After(timeout):
	}
}

func writeSchedule(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write schedule %s: %v", path, err)
	}
}

func scheduleBody(message string, cronExpr string, extra string) string {
	return "name: Demo\n" +
		"description: valid\n" +
		"enabled: true\n" +
		"cron: \"" + cronExpr + "\"\n" +
		extra +
		"agentKey: demo-agent\n" +
		"query:\n" +
		"  message: " + message + "\n"
}

func scheduleID(req api.QueryRequest) string {
	meta, ok := req.Params["__schedule"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := meta["scheduleId"].(string)
	return id
}
