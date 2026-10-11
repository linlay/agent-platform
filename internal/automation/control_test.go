package automation

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func controlService(t *testing.T) *Service {
	t.Helper()
	return &Service{Registry: NewRegistry(t.TempDir()), ReceiptDir: t.TempDir(), DefaultZoneID: "Asia/Shanghai"}
}
func createArgs() map[string]any {
	return map[string]any{"name": "工作日报", "agentKey": "assistant", "cron": "0 18 * * 1-5", "enabled": false, "query": map[string]any{"message": "  整理工作记录\n保持原文  "}}
}
func applyTestControl(t *testing.T, s *Service, action string, args map[string]any, key string) any {
	t.Helper()
	plan, err := s.PrepareControl(action, args, key)
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.ExecuteControl(action, args, key, func(d string) bool { return d == plan.Digest })
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func controlResult(t *testing.T, v any) (string, string) {
	t.Helper()
	b, _ := json.Marshal(v)
	var result struct {
		Automation api.AutomationDetailResponse `json:"automation"`
		Revision   string                       `json:"baseRevision"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result.Automation.ID, result.Revision
}
func TestControlCreateReplayUpdateAndDelete(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	plan, err := s.PrepareControl("create", args, "create-call")
	if err != nil {
		t.Fatal(err)
	}
	if plan.After.Query.AccessLevel != "default" || plan.After.ZoneID != "Asia/Shanghai" || len(plan.Preview) != 3 {
		t.Fatalf("preview: %+v", plan)
	}
	if _, err = s.ExecuteControl("create", args, "create-call", func(string) bool { return false }); err == nil {
		t.Fatal("missing approval accepted")
	}
	result := applyTestControl(t, s, "create", args, "create-call")
	id, revision := controlResult(t, result)
	// Replay survives a new service instance and never allocates another task.
	restarted := &Service{Registry: s.Registry, ReceiptDir: s.ReceiptDir, DefaultZoneID: s.DefaultZoneID}
	second := applyTestControl(t, restarted, "create", args, "create-call")
	id2, _ := controlResult(t, second)
	if id != id2 {
		t.Fatal("duplicate create")
	}
	defs, _ := s.Registry.Load()
	if len(defs) != 1 || defs[0].Query.Message != "  整理工作记录\n保持原文  " {
		t.Fatalf("definitions: %+v", defs)
	}
	args["name"] = "changed"
	if _, err = s.PrepareControl("create", args, "create-call"); err == nil {
		t.Fatal("changed invocation replayed")
	}
	patch := map[string]any{"id": id, "baseRevision": revision, "name": "新日报"}
	updated := applyTestControl(t, s, "update", patch, "update-call")
	_, newRevision := controlResult(t, updated)
	if revision == newRevision {
		t.Fatal("revision unchanged")
	}
	if _, err = s.PrepareControl("delete", map[string]any{"id": id, "baseRevision": revision}, "stale-call"); err == nil {
		t.Fatal("stale revision accepted")
	}
	remove := map[string]any{"id": id, "baseRevision": newRevision}
	applyTestControl(t, s, "delete", remove, "delete-call")
	applyTestControl(t, restarted, "delete", remove, "delete-call")
	defs, _ = s.Registry.Load()
	if len(defs) != 0 {
		t.Fatal("delete not applied")
	}
}
func TestControlConcurrentHTTPChangeInvalidatesApproval(t *testing.T) {
	s := controlService(t)
	id, rev := controlResult(t, applyTestControl(t, s, "create", createArgs(), "create"))
	args := map[string]any{"id": id, "baseRevision": rev, "enabled": true}
	p, err := s.PrepareControl("setEnabled", args, "enable")
	if err != nil {
		t.Fatal(err)
	}
	name := "Changed from Desktop"
	if _, err = s.UpdateAutomation(api.UpdateAutomationRequest{ID: id, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecuteControl("setEnabled", args, "enable", func(d string) bool { return d == p.Digest }); err == nil {
		t.Fatal("stale approval applied")
	}
	def, _ := s.FindAutomation(id)
	if def.Enabled || def.Name != name {
		t.Fatal("concurrent update overwritten")
	}
}
func TestControlInterruptedIntentDoesNotReplay(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	plan, err := s.PrepareControl("create", args, "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.receiptPath("interrupted")
	b, _ := json.Marshal(controlReceipt{RequestDigest: plan.RequestDigest, Plan: *plan, State: "started"})
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareControl("create", args, "interrupted"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected uncertain result: %v", err)
	}
	defs, _ := s.Registry.Load()
	if len(defs) != 0 {
		t.Fatal("interrupted request executed")
	}
}
func TestControlValidationRejectsInvalidOrLegacyFields(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["query"] = map[string]any{"message": "hello", "access_level": "full_access"} },
		func(a map[string]any) { a["cron"] = "0 0 9 * * *" },
		func(a map[string]any) { a["zoneId"] = "Invalid/Zone" },
		func(a map[string]any) { a["remainingRuns"] = 1.5 },
		func(a map[string]any) { a["query"] = map[string]any{"message": "hello", "accessLevel": "super"} },
	} {
		s := controlService(t)
		args := createArgs()
		change(args)
		if _, err := s.PrepareControl("create", args, ""); err == nil {
			t.Fatalf("invalid request accepted: %+v", args)
		}
	}
}

func TestControlRemainingRunsOmissionSetAndClear(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	args["remainingRuns"] = 2
	id, revision := controlResult(t, applyTestControl(t, s, "create", args, "create"))
	for _, tc := range []struct {
		name   string
		fields map[string]any
		want   int
	}{
		{"omit", map[string]any{"name": "Updated name"}, 2},
		{"set", map[string]any{"remainingRuns": 3}, 3},
		{"clear", map[string]any{"remainingRuns": nil}, 0},
	} {
		patch := tc.fields
		patch["id"], patch["baseRevision"] = id, revision
		updated := applyTestControl(t, s, "update", patch, tc.name)
		updatedID, nextRevision := controlResult(t, updated)
		if updatedID != id {
			t.Fatal("update recreated the task")
		}
		def, err := s.FindAutomation(id)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == 0 && def.RemainingRuns != nil || tc.want != 0 && (def.RemainingRuns == nil || *def.RemainingRuns != tc.want) {
			t.Fatalf("%s: wrong remainingRuns: %#v", tc.name, def.RemainingRuns)
		}
		revision = nextRevision
		if tc.want == 0 {
			data, err := os.ReadFile(def.SourceFile)
			if err != nil || strings.Contains(string(data), "remainingRuns:") {
				t.Fatalf("cleared limit remained in YAML: %s, %v", data, err)
			}
			applyTestControl(t, s, "update", patch, tc.name)
		}
	}
	for _, value := range []any{0, -1, 1.5, "3"} {
		if _, err := s.PrepareControl("update", map[string]any{"id": id, "baseRevision": revision, "remainingRuns": value}, ""); err == nil {
			t.Fatalf("invalid remainingRuns accepted: %v", value)
		}
	}
}

func TestControlTimezonePreservesSourceIntentAndBindsReview(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	id, revision := controlResult(t, applyTestControl(t, s, "create", args, "create"))
	def, err := s.FindAutomation(id)
	if err != nil || def.Environment.ZoneID != "" {
		t.Fatalf("implicit zone persisted: %+v, %v", def, err)
	}
	patch := map[string]any{"id": id, "baseRevision": revision, "name": "Changed"}
	approved, err := s.PrepareControl("update", patch, "update")
	if err != nil || approved.After.ZoneID != "Asia/Shanghai" {
		t.Fatalf("effective review zone: %+v, %v", approved, err)
	}
	s.DefaultZoneID = "UTC"
	if _, err := s.ExecuteControl("update", patch, "update", func(d string) bool { return d == approved.Digest }); err == nil {
		t.Fatal("changed effective timezone reused old approval")
	}
	_, revision = controlResult(t, applyTestControl(t, s, "update", patch, "update"))
	def, err = s.FindAutomation(id)
	if err != nil || def.Environment.ZoneID != "" {
		t.Fatalf("update fixed an implicit zone: %+v, %v", def, err)
	}
	patch = map[string]any{"id": id, "baseRevision": revision, "zoneId": "Asia/Tokyo"}
	_, revision = controlResult(t, applyTestControl(t, s, "update", patch, "fixed"))
	def, _ = s.FindAutomation(id)
	if def.Environment.ZoneID != "Asia/Tokyo" {
		t.Fatal("explicit timezone not persisted")
	}
	patch = map[string]any{"id": id, "baseRevision": revision, "zoneId": ""}
	applyTestControl(t, s, "update", patch, "follow-default")
	def, _ = s.FindAutomation(id)
	if def.Environment.ZoneID != "" {
		t.Fatal("explicit timezone was not cleared")
	}
}

func TestControlManualTriggerFreezesReviewedZoneWithoutSavingIt(t *testing.T) {
	s := controlService(t)
	id, revision := controlResult(t, applyTestControl(t, s, "create", createArgs(), "create"))
	store, err := NewExecutionStore(t.TempDir(), "executions.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
		return successfulTestQuery(req, hooks), nil
	}, nil, synchronousExecutionRecorder{store: store})
	// A different scheduler default must not replace the zone shown in review.
	s.Orchestrator = NewOrchestrator(s.Registry, dispatcher, config.AutomationConfig{DefaultZoneID: "UTC"})
	if err := s.Orchestrator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-s.Orchestrator.Stop().Done() }()
	result := applyTestControl(t, s, "trigger", map[string]any{"id": id, "baseRevision": revision}, "trigger").(api.TriggerAutomationResponse)
	execution, err := store.GetExecution(result.ExecutionID)
	if err != nil || execution == nil || execution.ZoneID != "Asia/Shanghai" {
		t.Fatalf("wrong execution timezone snapshot: %+v, %v", execution, err)
	}
	def, err := s.FindAutomation(id)
	if err != nil || def.Environment.ZoneID != "" {
		t.Fatalf("manual trigger fixed the source timezone: %+v, %v", def, err)
	}
}
func TestControlTriggerReplayDispatchesOnce(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	args["remainingRuns"] = 3
	id, rev := controlResult(t, applyTestControl(t, s, "create", args, "create"))
	var count atomic.Int32
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, _ QueryRunHooks) (QueryRunResult, error) {
		count.Add(1)
		if req.AccessLevel != "default" {
			t.Errorf("unexpected access level: %s", req.AccessLevel)
		}
		return successfulTestQuery(req, QueryRunHooks{}), nil
	}, nil, nil)
	s.Orchestrator = NewOrchestrator(s.Registry, dispatcher, config.AutomationConfig{DefaultZoneID: "Asia/Shanghai"})
	if err := s.Orchestrator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { <-s.Orchestrator.Stop().Done() }()
	trigger := map[string]any{"id": id, "baseRevision": rev}
	plan, err := s.PrepareControl("trigger", trigger, "trigger")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ExecuteControl("trigger", trigger, "trigger", func(d string) bool { return d == plan.Digest }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(time.Second)
	for count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if count.Load() != 1 {
		t.Fatalf("dispatch count %d", count.Load())
	}
	def, _ := s.FindAutomation(id)
	if def.Enabled || def.RemainingRuns == nil || *def.RemainingRuns != 3 {
		t.Fatal("manual run changed schedule")
	}
}

func TestControlSourceChangeAtCommitIsNotOverwritten(t *testing.T) {
	s := controlService(t)
	id, rev := controlResult(t, applyTestControl(t, s, "create", createArgs(), "create"))
	args := map[string]any{"id": id, "baseRevision": rev, "name": "approved name"}
	p, err := s.PrepareControl("update", args, "update")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ExecuteControl("update", args, "update", func(digest string) bool {
		def, err := s.FindAutomation(id)
		if err != nil {
			t.Fatal(err)
		}
		def.Name = "source editor won"
		if err = s.Registry.Persist(def); err != nil {
			t.Fatal(err)
		}
		return digest == p.Digest
	})
	if err == nil {
		t.Fatal("source conflict accepted")
	}
	def, _ := s.FindAutomation(id)
	if def.Name != "source editor won" {
		t.Fatal("source edit overwritten")
	}
}
func TestControlPreviewHonorsRemainingRuns(t *testing.T) {
	s := controlService(t)
	args := createArgs()
	args["remainingRuns"] = 1
	p, err := s.PrepareControl("create", args, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Preview) != 1 {
		t.Fatalf("preview exceeded remaining runs: %v", p.Preview)
	}
}
