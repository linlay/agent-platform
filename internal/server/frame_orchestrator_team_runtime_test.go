package server

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type teamConcurrencyTracker struct {
	mu      sync.Mutex
	active  int
	maximum int
	started []string
	startCh chan string
	release <-chan struct{}
}

func (t *teamConcurrencyTracker) enter(memberKey string) {
	t.mu.Lock()
	t.active++
	if t.active > t.maximum {
		t.maximum = t.active
	}
	t.started = append(t.started, memberKey)
	t.mu.Unlock()
	t.startCh <- memberKey
	<-t.release
	t.mu.Lock()
	t.active--
	t.mu.Unlock()
}

func (t *teamConcurrencyTracker) snapshot() (started []string, maximum int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.started...), t.maximum
}

type gatedTeamMemberStream struct {
	memberKey string
	answer    string
	tracker   *teamConcurrencyTracker
	entered   bool
}

func (s *gatedTeamMemberStream) Next() (contracts.AgentDelta, error) {
	if s.entered {
		return nil, io.EOF
	}
	s.entered = true
	s.tracker.enter(s.memberKey)
	return contracts.DeltaContent{Text: s.answer}, nil
}

func (s *gatedTeamMemberStream) Close() error { return nil }

func (s *gatedTeamMemberStream) InjectToolResult(string, string, bool) bool { return false }

func (s *gatedTeamMemberStream) FinalAssistantContent() (string, bool) {
	return s.answer, strings.TrimSpace(s.answer) != ""
}

func replaceTeamRuntimeSnapshot(o *frameOrchestrator, keys []string, maxParallel int, defs map[string]catalog.AgentDefinition) {
	snapshot := catalog.NewTeamSnapshot(catalog.TeamDefinition{
		TeamID:      "research",
		Name:        "Research",
		RuntimeMode: catalog.TeamRuntimeModeOrchestrated,
		AgentKeys:   append([]string(nil), keys...),
		Orchestrator: catalog.TeamOrchestratorConfig{
			ModelKey: "mock-model", MaxParallel: maxParallel,
		},
	}, defs)
	o.teamSnapshot = &snapshot
}

func TestTeamFanoutRunsEveryMemberThroughBoundedPool(t *testing.T) {
	keys := []string{"writer", "reviewer", "researcher", "publisher"}
	defs := make(map[string]catalog.AgentDefinition, len(keys))
	tasks := make([]contracts.SubAgentTaskSpec, 0, len(keys))
	children := make(map[string]contracts.AgentStream, len(keys))
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	tracker := &teamConcurrencyTracker{
		startCh: make(chan string, len(keys)),
		release: release,
	}
	for _, key := range keys {
		defs[key] = catalog.AgentDefinition{Key: key, Name: strings.ToUpper(key), Mode: "REACT"}
		tasks = append(tasks, contracts.SubAgentTaskSpec{SubAgentKey: key})
		children[key] = &gatedTeamMemberStream{memberKey: key, answer: key + " answer", tracker: tracker}
	}
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "fanout", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout, Tasks: tasks,
	}}}
	engine := &orchestratorAgentEngine{streamsByAgentKey: children}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, children, defs, &routed, &emitted)
	o.agent = engine
	replaceTeamRuntimeSnapshot(o, keys, 2, defs)

	type runResult struct {
		failed      bool
		interrupted bool
		err         error
	}
	done := make(chan runResult, 1)
	go func() {
		failed, interrupted, err := o.Run(main)
		done <- runResult{failed: failed, interrupted: interrupted, err: err}
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-tracker.startCh:
		case <-time.After(time.Second):
			t.Fatal("first bounded fanout batch did not start")
		}
	}
	select {
	case member := <-tracker.startCh:
		t.Fatalf("member %q started before a maxParallel=2 slot was released", member)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })

	select {
	case result := <-done:
		if result.err != nil || result.failed || result.interrupted {
			t.Fatalf("Run() = failed=%v interrupted=%v err=%v", result.failed, result.interrupted, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fanout did not finish after releasing the bounded pool")
	}
	started, maximum := tracker.snapshot()
	if len(started) != len(keys) {
		t.Fatalf("fanout started %d members, want all %d: %v", len(started), len(keys), started)
	}
	if maximum > 2 {
		t.Fatalf("fanout maximum concurrency=%d, want <=2", maximum)
	}
	if len(engine.streamsByAgentKey) != 0 {
		t.Fatalf("fanout left unexecuted members: %v", engine.streamsByAgentKey)
	}
	if len(main.injected) != 1 || main.injected[0].isError || !main.finalResponseRequired {
		t.Fatalf("unexpected fanout completion: injected=%#v finalRequired=%v", main.injected, main.finalResponseRequired)
	}
}

func TestTeamFanoutPartialFailureDoesNotCancelOtherMembers(t *testing.T) {
	keys := []string{"writer", "reviewer", "publisher"}
	defs := map[string]catalog.AgentDefinition{
		"writer":    {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer":  {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
		"publisher": {Key: "publisher", Name: "Publisher", Mode: "REACT"},
	}
	children := map[string]contracts.AgentStream{
		"writer":    &stubOrchestratableStream{finalText: "draft ready"},
		"reviewer":  &stubOrchestratableStream{},
		"publisher": &stubOrchestratableStream{finalText: "published"},
	}
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "fanout", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}, {SubAgentKey: "publisher"}},
	}}}
	engine := &orchestratorAgentEngine{streamsByAgentKey: children}
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, children, defs, nil, &emitted)
	o.agent = engine
	replaceTeamRuntimeSnapshot(o, keys, 2, defs)

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(engine.streamsByAgentKey) != 0 {
		t.Fatalf("partial failure cancelled or skipped members: %v", engine.streamsByAgentKey)
	}
	if len(main.injected) != 1 || !main.injected[0].isError || !main.finalResponseRequired {
		t.Fatalf("unexpected partial fanout completion: injected=%#v finalRequired=%v", main.injected, main.finalResponseRequired)
	}
	var results []childTaskResult
	if err := json.Unmarshal([]byte(main.injected[0].text), &results); err != nil {
		t.Fatalf("decode fanout aggregate: %v", err)
	}
	if len(results) != 3 || results[0].Status != "completed" || results[1].Status != "failed" || results[2].Status != "completed" {
		t.Fatalf("fanout did not retain both successes around failure: %#v", results)
	}
	complete, failedLifecycle := 0, 0
	for _, delta := range emitted {
		lifecycle, ok := delta.(contracts.DeltaTaskLifecycle)
		if !ok {
			continue
		}
		switch lifecycle.Kind {
		case "complete":
			complete++
		case "error":
			failedLifecycle++
		}
	}
	if complete != 2 || failedLifecycle != 1 {
		t.Fatalf("terminal lifecycle counts complete=%d error=%d: %#v", complete, failedLifecycle, emitted)
	}
}

func TestTeamDirectSuccessDoesNotConsumeCoordinatorRewrite(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{
		contracts.DeltaTeamDispatch{
			MainToolID: "direct", Kind: agentteam.DispatchKindDirect, DelegateMode: agentteam.DelegateModeDirect,
			Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}},
		},
		contracts.DeltaContent{Text: "coordinator must not rewrite the member answer"},
	}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	child := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "member answer"}}, finalText: "member answer"}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, map[string]contracts.AgentStream{"writer": child}, defs, &routed, &emitted)

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if main.index != 1 {
		t.Fatalf("direct success consumed %d coordinator deltas, want only the dispatch", main.index)
	}
	if len(main.injected) != 0 || main.finalResponseRequired || main.optionalToolsAllowed {
		t.Fatalf("direct success returned control to coordinator: %#v", main)
	}
	for _, delta := range emitted {
		if content, ok := delta.(contracts.DeltaContent); ok && strings.Contains(content.Text, "coordinator") {
			t.Fatalf("coordinator rewrite leaked after direct success: %#v", emitted)
		}
	}
}

func TestTeamRuntimeRejectsNestedTeamAndAgentInvokeMembers(t *testing.T) {
	t.Run("TEAM member", func(t *testing.T) {
		defs := map[string]catalog.AgentDefinition{
			"nested": {Key: "nested", Name: "Nested Team", Mode: agentteam.Mode},
		}
		children := map[string]contracts.AgentStream{
			"nested": &stubOrchestratableStream{finalText: "must not run"},
		}
		main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
			MainToolID: "direct", Kind: agentteam.DispatchKindDirect, DelegateMode: agentteam.DelegateModeDirect,
			Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "nested"}},
		}}}
		engine := &orchestratorAgentEngine{streamsByAgentKey: children}
		o := newTeamFrameOrchestrator(t, main, children, defs, nil, nil)
		o.agent = engine
		replaceTeamRuntimeSnapshot(o, []string{"nested"}, 1, defs)

		failed, interrupted, err := o.Run(main)
		if err != nil || failed || interrupted {
			t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
		}
		if len(main.injected) != 1 || !main.injected[0].isError || !strings.Contains(main.injected[0].text, "cannot run as a Team child") {
			t.Fatalf("nested TEAM was not rejected: %#v", main.injected)
		}
		if len(engine.streamsByAgentKey) != 1 {
			t.Fatal("nested TEAM member stream was executed")
		}
	})

	t.Run("member carrying agent_invoke", func(t *testing.T) {
		defs := map[string]catalog.AgentDefinition{
			"writer": {
				Key: "writer", Name: "Writer", Mode: "REACT", VisibilityScopes: []string{"invoke"},
				Tools: []string{contracts.InvokeAgentsToolName},
			},
		}
		children := map[string]contracts.AgentStream{
			"writer": &stubOrchestratableStream{finalText: "must not run"},
		}
		main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
			MainToolID: "invoke", Kind: agentteam.DispatchKindInvoke,
			Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer", TaskText: "nested work"}},
		}}}
		engine := &orchestratorAgentEngine{streamsByAgentKey: children}
		o := newTeamFrameOrchestrator(t, main, children, defs, nil, nil)
		o.agent = engine
		replaceTeamRuntimeSnapshot(o, []string{"writer"}, 1, defs)

		failed, interrupted, err := o.Run(main)
		if err != nil || failed || interrupted {
			t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
		}
		if len(main.injected) != 1 || !main.injected[0].isError || !strings.Contains(main.injected[0].text, "nested sub-agent invocation is not allowed") {
			t.Fatalf("agent_invoke member was not rejected: %#v", main.injected)
		}
		if len(engine.streamsByAgentKey) != 1 {
			t.Fatal("agent_invoke member stream was executed")
		}
	})
}

var _ contracts.OrchestratableAgentStream = (*gatedTeamMemberStream)(nil)
