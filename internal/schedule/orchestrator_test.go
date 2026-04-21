package schedule

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

type fakeTeamLookup struct {
	teams map[string]catalog.TeamDefinition
}

func (f fakeTeamLookup) TeamDefinition(teamID string) (catalog.TeamDefinition, bool) {
	def, ok := f.teams[teamID]
	return def, ok
}

func TestParseCronScheduleAcceptsTraditionalFiveField(t *testing.T) {
	valid := []string{"0 9 * * *", "17 9 * * *", "*/5 * * * *"}
	for _, spec := range valid {
		if _, err := parseCronSchedule(spec); err != nil {
			t.Fatalf("expected %q to be valid: %v", spec, err)
		}
	}
}

func TestParseCronScheduleRejectsSixField(t *testing.T) {
	invalid := []string{"0 0 9 * * *", "*/1 * * * * *"}
	for _, spec := range invalid {
		if _, err := parseCronSchedule(spec); err == nil {
			t.Fatalf("expected %q to be rejected", spec)
		}
	}
}

func TestRegistryLoadsStructuredScheduleDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daily.demo.yml"), []byte(
		"name: Daily Demo\n"+
			"description: Demo schedule\n"+
			"enabled: true\n"+
			"cron: \"*/5 * * * *\"\n"+
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
	if !def.Enabled || def.Cron != "*/5 * * * *" || def.AgentKey != "demo-agent" {
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
			"cron: \"17 9 * * *\"\n"+
			"agentKey: demo-agent\n"+
			"query:\n"+
			"  message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.example.yml"), []byte(
		"name: Ignored\n"+
			"description: ignored\n"+
			"cron: \"17 9 * * *\"\n"+
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

func TestRegistryLoadsNestedScheduleDefinition(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "daily")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested dirs: %v", err)
	}
	writeSchedule(t, filepath.Join(nested, "demo.yml"), scheduleBody("hello", "17 9 * * *", ""))

	defs, err := NewRegistry(root, nil).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "demo" {
		t.Fatalf("expected nested schedule to load, got %#v", defs)
	}
}

func TestRegistryKeepsLexicallyFirstDuplicateScheduleID(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "a")
	secondDir := filepath.Join(root, "b")
	if err := os.MkdirAll(firstDir, 0o755); err != nil {
		t.Fatalf("create first dir: %v", err)
	}
	if err := os.MkdirAll(secondDir, 0o755); err != nil {
		t.Fatalf("create second dir: %v", err)
	}
	writeSchedule(t, filepath.Join(firstDir, "daily.yml"), scheduleBodyWithDescription("first", "17 9 * * *", "", "first"))
	writeSchedule(t, filepath.Join(secondDir, "daily.demo.yml"), scheduleBodyWithDescription("second", "17 9 * * *", "", "second"))

	defs, err := NewRegistry(root, nil).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected one schedule after duplicate resolution, got %#v", defs)
	}
	if defs[0].ID != "daily" || defs[0].Query.Message != "first" || defs[0].Description != "first" {
		t.Fatalf("expected lexically first schedule to win, got %#v", defs[0])
	}
}

func TestRegistrySkipsInvalidSchedules(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"missing-message.yml":       "name: Missing Message\ndescription: bad\ncron: \"17 9 * * *\"\nagentKey: demo-agent\nquery:\n  hidden: true\n",
		"invalid-chat.yml":          "name: Invalid Chat\ndescription: bad\ncron: \"17 9 * * *\"\nagentKey: demo-agent\nquery:\n  message: hi\n  chatId: \"bad chat id!\"\n",
		"invalid-zone.yml":          "name: Invalid Zone\ndescription: bad\ncron: \"17 9 * * *\"\nagentKey: demo-agent\nenvironment:\n  zoneId: Mars/Base\nquery:\n  message: hi\n",
		"invalid-cron.yml":          "name: Invalid Cron\ndescription: bad\ncron: \"0 0 9 * * *\"\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-text-cron.yml":     "name: Invalid Text Cron\ndescription: bad\ncron: \"nope\"\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-zero-runs.yml":     "name: Invalid Runs Zero\ndescription: bad\ncron: \"17 9 * * *\"\nremainingRuns: 0\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-negative-runs.yml": "name: Invalid Runs Negative\ndescription: bad\ncron: \"17 9 * * *\"\nremainingRuns: -1\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"invalid-text-runs.yml":     "name: Invalid Runs Text\ndescription: bad\ncron: \"17 9 * * *\"\nremainingRuns: nope\nagentKey: demo-agent\nquery:\n  message: hi\n",
		"valid.yml":                 "name: Valid\ndescription: ok\ncron: \"17 9 * * *\"\nremainingRuns: 2\nagentKey: demo-agent\nquery:\n  message: hi\n",
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

func TestRegistryRejectsSixFieldCronWithHelpfulError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "invalid.yml")
	writeSchedule(t, path, scheduleBody("hello", "0 0 9 * * *", ""))

	_, err := NewRegistry(root, nil).parseDefinition(path)
	if err == nil {
		t.Fatal("expected six-field cron to fail")
	}
	if !strings.Contains(err.Error(), "only traditional 5-field cron") {
		t.Fatalf("expected helpful error, got %v", err)
	}
}

func TestRegistryValidatesTeamScopedSchedule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "valid.yml"), []byte(
		"name: Team Valid\n"+
			"description: ok\n"+
			"cron: \"17 9 * * *\"\n"+
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
			"cron: \"17 9 * * *\"\n"+
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

func TestOrchestratorRegistersEnabledCronSchedule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"name: Demo\n"+
			"description: valid\n"+
			"enabled: true\n"+
			"cron: \"17 9 * * *\"\n"+
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
			"cron: \"17 9 * * *\"\n"+
			"agentKey: demo-agent\n"+
			"query:\n"+
			"  message: skipped\n",
	), 0o644); err != nil {
		t.Fatalf("write disabled schedule file: %v", err)
	}

	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, _ api.QueryRequest) error {
		return nil
	}, nil), config.ScheduleConfig{})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	reg := waitForRegistration(t, orchestrator, "demo", 2*time.Second)
	if reg.Definition.Query.Message != "hello" {
		t.Fatalf("unexpected registration %#v", reg.Definition)
	}
	waitForNoRegistration(t, orchestrator, "disabled", 500*time.Millisecond)
}

func TestOrchestratorConsumesRemainingRunsAndDeletesFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "limited.yml")
	writeSchedule(t, path, scheduleBody("hello", "17 9 * * *", "remainingRuns: 2\n"))

	dispatched := make(chan api.QueryRequest, 4)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		dispatched <- req
		return nil
	}, nil), config.ScheduleConfig{})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	reg := waitForRegistration(t, orchestrator, "limited", 2*time.Second)
	stop, err := orchestrator.fire(reg)
	if err != nil {
		t.Fatalf("first fire: %v", err)
	}
	if stop {
		t.Fatal("expected schedule to remain after first fire")
	}
	req := waitForRequest(t, dispatched, time.Second)
	if req.Message != "hello" {
		t.Fatalf("unexpected request %#v", req)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schedule after first run: %v", err)
	}
	if !strings.Contains(string(data), "remainingRuns: 1") {
		t.Fatalf("expected remainingRuns to be decremented, got:\n%s", string(data))
	}

	reg = waitForRegistration(t, orchestrator, "limited", 2*time.Second)
	stop, err = orchestrator.fire(reg)
	if err != nil {
		t.Fatalf("second fire: %v", err)
	}
	if !stop {
		t.Fatal("expected schedule to stop after second fire")
	}
	waitForRequest(t, dispatched, time.Second)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected schedule file deleted, got err=%v", err)
	}
	waitForNoRegistration(t, orchestrator, "limited", 2*time.Second)
}

func TestOrchestratorConsumesRunOnDispatchFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "failing.yml")
	writeSchedule(t, path, scheduleBody("boom", "17 9 * * *", "remainingRuns: 1\n"))

	expectedErr := errors.New("dispatch failed")
	attempts := make(chan api.QueryRequest, 2)
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		attempts <- req
		return expectedErr
	}, nil), config.ScheduleConfig{})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	reg := waitForRegistration(t, orchestrator, "failing", 2*time.Second)
	stop, err := orchestrator.fire(reg)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected dispatch error, got %v", err)
	}
	if !stop {
		t.Fatal("expected last run to stop schedule")
	}
	waitForRequest(t, attempts, time.Second)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected schedule file deleted after failed dispatch, got err=%v", err)
	}
	waitForNoRegistration(t, orchestrator, "failing", 2*time.Second)
}

func TestOrchestratorWatchesScheduleDirectory(t *testing.T) {
	root := t.TempDir()
	orchestrator := NewOrchestrator(NewRegistry(root, nil), NewDispatcher(func(_ context.Context, _ api.QueryRequest) error {
		return nil
	}, nil), config.ScheduleConfig{})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	original := filepath.Join(nested, "demo.yml")
	writeSchedule(t, original, scheduleBody("first", "17 9 * * *", ""))

	reg := waitForRegistrationMatch(t, orchestrator, "demo", 2*time.Second, func(reg *Registration) bool {
		return reg.Definition.Query.Message == "first"
	})
	if reg.Definition.Query.Message != "first" {
		t.Fatalf("unexpected registration %#v", reg.Definition)
	}

	writeSchedule(t, original, scheduleBody("second", "23 10 * * *", ""))
	reg = waitForRegistrationMatch(t, orchestrator, "demo", 2*time.Second, func(reg *Registration) bool {
		return reg.Definition.Query.Message == "second" && reg.Definition.Cron == "23 10 * * *"
	})
	if reg.Definition.Query.Message != "second" || reg.Definition.Cron != "23 10 * * *" {
		t.Fatalf("expected updated registration, got %#v", reg.Definition)
	}

	renamed := filepath.Join(root, "renamed.yml")
	if err := os.Rename(original, renamed); err != nil {
		t.Fatalf("rename schedule: %v", err)
	}
	waitForNoRegistration(t, orchestrator, "demo", 2*time.Second)
	reg = waitForRegistrationMatch(t, orchestrator, "renamed", 2*time.Second, func(reg *Registration) bool {
		return reg.Definition.Query.Message == "second"
	})
	if reg.Definition.Query.Message != "second" {
		t.Fatalf("unexpected renamed registration %#v", reg.Definition)
	}

	if err := os.Remove(renamed); err != nil {
		t.Fatalf("remove schedule: %v", err)
	}
	waitForNoRegistration(t, orchestrator, "renamed", 2*time.Second)
}

func TestOrchestratorUsesDefaultZoneIDWhenScheduleZoneMissing(t *testing.T) {
	root := t.TempDir()
	writeSchedule(t, filepath.Join(root, "demo.yml"), scheduleBody("hello", "17 9 * * *", ""))

	orchestrator := NewOrchestrator(
		NewRegistry(root, nil),
		NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return nil }, nil),
		config.ScheduleConfig{DefaultZoneID: "Asia/Shanghai"},
	)
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	reg := waitForRegistration(t, orchestrator, "demo", 2*time.Second)
	if reg.location == nil || reg.location.String() != "Asia/Shanghai" {
		t.Fatalf("expected default zone to apply, got %#v", reg.location)
	}
}

func TestOrchestratorLimitsDispatchConcurrency(t *testing.T) {
	root := t.TempDir()
	orchestrator := NewOrchestrator(
		NewRegistry(root, nil),
		NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return nil }, nil),
		config.ScheduleConfig{PoolSize: 1},
	)

	regCtx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	regCtx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	reg1 := &Registration{
		Definition: Definition{ID: "one", Enabled: true, Query: Query{Message: "first"}},
		ctx:        regCtx1,
		cancel:     cancel1,
	}
	reg2 := &Registration{
		Definition: Definition{ID: "two", Enabled: true, Query: Query{Message: "second"}},
		ctx:        regCtx2,
		cancel:     cancel2,
	}

	orchestrator.registrations["one"] = reg1
	orchestrator.registrations["two"] = reg2

	release := make(chan struct{})
	entered := make(chan string, 2)
	var current int32
	var maxConcurrent int32
	orchestrator.dispatcher = NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		next := atomic.AddInt32(&current, 1)
		for {
			prev := atomic.LoadInt32(&maxConcurrent)
			if next <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, next) {
				break
			}
		}
		entered <- req.Message
		<-release
		atomic.AddInt32(&current, -1)
		return nil
	}, nil)

	done := make(chan error, 2)
	go func() {
		_, err := orchestrator.fire(reg1)
		done <- err
	}()
	go func() {
		_, err := orchestrator.fire(reg2)
		done <- err
	}()

	first := waitForMessage(t, entered, time.Second)
	if first != "first" && first != "second" {
		t.Fatalf("unexpected first dispatched message %q", first)
	}
	select {
	case second := <-entered:
		t.Fatalf("expected second dispatch to wait for pool slot, got %q", second)
	case <-time.After(150 * time.Millisecond):
	}
	if atomic.LoadInt32(&maxConcurrent) != 1 {
		t.Fatalf("expected max concurrency 1, got %d", atomic.LoadInt32(&maxConcurrent))
	}

	release <- struct{}{}
	second := waitForMessage(t, entered, time.Second)
	if second == first {
		t.Fatalf("expected different second dispatch, got %q twice", second)
	}
	release <- struct{}{}

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("fire returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for fire completion")
		}
	}
}

func TestOrchestratorReleasesDispatchSlotAfterDispatchFailure(t *testing.T) {
	root := t.TempDir()
	orchestrator := NewOrchestrator(
		NewRegistry(root, nil),
		NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return nil }, nil),
		config.ScheduleConfig{PoolSize: 1},
	)

	reg1 := &Registration{Definition: Definition{ID: "one", Enabled: true, Query: Query{Message: "first"}}, ctx: context.Background()}
	reg2 := &Registration{Definition: Definition{ID: "two", Enabled: true, Query: Query{Message: "second"}}, ctx: context.Background()}
	orchestrator.registrations["one"] = reg1
	orchestrator.registrations["two"] = reg2

	entered := make(chan string, 2)
	release := make(chan struct{})
	var calls int32
	orchestrator.dispatcher = NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		entered <- req.Message
		<-release
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("dispatch failed")
		}
		return nil
	}, nil)

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() {
		_, err := orchestrator.fire(reg1)
		done1 <- err
	}()
	first := waitForMessage(t, entered, time.Second)
	if first != "first" {
		t.Fatalf("expected first registration to dispatch first, got %q", first)
	}
	go func() {
		_, err := orchestrator.fire(reg2)
		done2 <- err
	}()

	release <- struct{}{}
	if err := <-done1; err == nil {
		t.Fatal("expected first dispatch to fail")
	}
	second := waitForMessage(t, entered, time.Second)
	if second != "second" {
		t.Fatalf("expected second registration to acquire released slot, got %q", second)
	}
	release <- struct{}{}
	if err := <-done2; err != nil {
		t.Fatalf("expected second dispatch to succeed, got %v", err)
	}
}

func TestAcquireDispatchSlotContextCancellationDoesNotLeak(t *testing.T) {
	orchestrator := NewOrchestrator(
		NewRegistry(t.TempDir(), nil),
		nil,
		config.ScheduleConfig{PoolSize: 1},
	)
	orchestrator.dispatchSlots <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if orchestrator.acquireDispatchSlot(ctx) {
		t.Fatal("expected acquire to fail after context cancellation")
	}

	orchestrator.releaseDispatchSlot()
	if !orchestrator.acquireDispatchSlot(context.Background()) {
		t.Fatal("expected slot to remain available after canceled acquire")
	}
	orchestrator.releaseDispatchSlot()
}

func TestWatcherIgnoresDSStoreChangesButReloadsRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	orchestrator := NewOrchestrator(
		NewRegistry(root, nil),
		nil,
		config.ScheduleConfig{PoolSize: 1},
	)
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer waitForStop(t, orchestrator)

	buf.Reset()

	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("finder"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}
	time.Sleep(reloadDebounce + 300*time.Millisecond)
	if strings.Contains(buf.String(), "registry ready count=") {
		t.Fatalf("expected .DS_Store change to be ignored, got logs %q", buf.String())
	}

	writeSchedule(t, filepath.Join(root, "demo.yml"), scheduleBody("hello", "17 9 * * *", ""))
	waitForRegistration(t, orchestrator, "demo", 3*time.Second)
	if !strings.Contains(buf.String(), "registered id=demo") {
		t.Fatalf("expected runtime file change to trigger reload, got logs %q", buf.String())
	}
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

func waitForRequest(t *testing.T, ch <-chan api.QueryRequest, timeout time.Duration) api.QueryRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(timeout):
		t.Fatal("timed out waiting for scheduled dispatch")
		return api.QueryRequest{}
	}
}

func waitForMessage(t *testing.T, ch <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timed out waiting for dispatched message")
		return ""
	}
}

func waitForRegistration(t *testing.T, orchestrator *Orchestrator, id string, timeout time.Duration) *Registration {
	t.Helper()
	return waitForRegistrationMatch(t, orchestrator, id, timeout, nil)
}

func waitForRegistrationMatch(t *testing.T, orchestrator *Orchestrator, id string, timeout time.Duration, match func(*Registration) bool) *Registration {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		orchestrator.mu.Lock()
		reg := orchestrator.registrations[id]
		orchestrator.mu.Unlock()
		if reg != nil && (match == nil || match(reg)) {
			return reg
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for registration %q", id)
	return nil
}

func waitForNoRegistration(t *testing.T, orchestrator *Orchestrator, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		orchestrator.mu.Lock()
		reg := orchestrator.registrations[id]
		orchestrator.mu.Unlock()
		if reg == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for registration %q to disappear", id)
}

func writeSchedule(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write schedule %s: %v", path, err)
	}
}

func scheduleBody(message string, cronExpr string, extra string) string {
	return scheduleBodyWithDescription(message, cronExpr, extra, "valid")
}

func scheduleBodyWithDescription(message string, cronExpr string, extra string, description string) string {
	return "name: Demo\n" +
		"description: " + description + "\n" +
		"enabled: true\n" +
		"cron: \"" + cronExpr + "\"\n" +
		extra +
		"agentKey: demo-agent\n" +
		"query:\n" +
		"  message: " + message + "\n"
}
