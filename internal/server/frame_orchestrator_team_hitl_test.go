package server

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/adapter"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/view"
)

type teamHITLChildSubmit struct {
	agentKey string
	request  api.SubmitRequest
}

type teamHITLTestEngine struct {
	submits    chan teamHITLChildSubmit
	interrupts chan string
	// formView switches members from a question to a connector form awaiting.
	formView *view.Reference
}

func (e *teamHITLTestEngine) Stream(ctx context.Context, req api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	return &teamHITLTestStream{
		ctx:        ctx,
		control:    contracts.RunControlFromContext(ctx),
		agentKey:   req.AgentKey,
		submits:    e.submits,
		interrupts: e.interrupts,
		formView:   e.formView,
	}, nil
}

type teamHITLTestStream struct {
	ctx        context.Context
	control    *contracts.RunControl
	agentKey   string
	submits    chan teamHITLChildSubmit
	interrupts chan string
	formView   *view.Reference
	step       int
}

func (s *teamHITLTestStream) Next() (contracts.AgentDelta, error) {
	switch s.step {
	case 0:
		s.step++
		if s.formView != nil {
			s.control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "raw_await", Mode: "form", ItemCount: 1})
			return contracts.DeltaAwaitAsk{
				AwaitingID: "raw_await",
				Mode:       "form",
				RunID:      "run_1",
				View:       s.formView,
				Form:       map[string]any{"title": "Edit " + s.agentKey, "data": map[string]any{"name": "original"}},
			}, nil
		}
		s.control.ExpectSubmit(contracts.AwaitingSubmitContext{
			AwaitingID: "raw_await",
			Mode:       "question",
			ItemCount:  1,
			Questions:  []any{map[string]any{"id": "q1", "question": "Answer for " + s.agentKey, "type": "text"}},
		})
		return contracts.DeltaAwaitAsk{
			AwaitingID: "raw_await",
			Mode:       "question",
			RunID:      "run_1",
			Questions:  []any{map[string]any{"id": "q1", "question": "Answer for " + s.agentKey, "type": "text"}},
		}, nil
	case 1:
		s.step++
		result, err := s.control.AwaitSubmitWithTimeout(s.ctx, "raw_await", 0)
		if err != nil {
			if s.interrupts != nil {
				s.interrupts <- s.agentKey
			}
			return nil, err
		}
		if s.submits != nil {
			s.submits <- teamHITLChildSubmit{agentKey: s.agentKey, request: result.Request}
		}
		return contracts.DeltaRequestSubmit{
			RequestID:  "req-" + s.agentKey,
			ChatID:     "chat_1",
			RunID:      "run_1",
			AwaitingID: "raw_await",
			SubmitID:   result.Request.SubmitID,
			Input:      result.Request.Input(),
		}, nil
	case 2:
		s.step++
		mode := "question"
		if s.formView != nil {
			mode = "form"
		}
		return contracts.DeltaAwaitingAnswer{
			AwaitingID: "raw_await",
			Answer:     map[string]any{"mode": mode, "status": "answered"},
		}, nil
	case 3:
		s.step++
		return contracts.DeltaContent{Text: s.agentKey + " completed"}, nil
	default:
		return nil, io.EOF
	}
}

func (s *teamHITLTestStream) Close() error { return nil }

func (s *teamHITLTestStream) InjectToolResult(string, string, bool) bool { return false }

func (s *teamHITLTestStream) FinalAssistantContent() (string, bool) {
	return s.agentKey + " completed", true
}

// teamHITLQueueProbe answers every published member awaiting and fails the test
// when a second awaiting is published before the previous one is answered.
type teamHITLQueueProbe struct {
	t           *testing.T
	control     *contracts.RunControl
	routed      *[]stream.StreamInput
	submit      func(ask stream.AwaitAsk) api.SubmitRequest
	asks        []stream.AwaitAsk
	outstanding int
}

func (p *teamHITLQueueProbe) emit(inputs ...stream.StreamInput) {
	*p.routed = append(*p.routed, inputs...)
	for _, input := range inputs {
		switch value := input.(type) {
		case stream.AwaitingAnswer:
			p.outstanding--
		case stream.AwaitAsk:
			if p.outstanding != 0 {
				p.t.Fatalf("awaiting %s published while another member awaiting is unanswered", value.AwaitingID)
			}
			if value.TaskID == "" || value.AwaitingID != value.TaskID+":raw_await" {
				p.t.Fatalf("member awaiting must carry its task and public ID: %#v", value)
			}
			p.outstanding++
			p.asks = append(p.asks, value)
			request := p.submit(value)
			request.ChatID, request.RunID, request.TeamID, request.AwaitingID = "chat_1", "run_1", "research", value.AwaitingID
			if ack := p.control.ResolveSubmit(request); !ack.Accepted {
				p.t.Fatalf("member submit not accepted: %#v", ack)
			}
		}
	}
}

func teamQuestionSubmit(t *testing.T, submitID string) func(stream.AwaitAsk) api.SubmitRequest {
	return func(ask stream.AwaitAsk) api.SubmitRequest {
		params, err := api.EncodeSubmitParams([]map[string]any{{"id": "q1", "answer": "answer for " + ask.TaskID}})
		if err != nil {
			t.Fatalf("encode member submit: %v", err)
		}
		return api.SubmitRequest{SubmitID: submitID + "-" + ask.TaskID, Params: params}
	}
}

func TestFrameOrchestratorTeamDelegationPublishesMemberHITLOneAtATime(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	engine := &teamHITLTestEngine{submits: make(chan teamHITLChildSubmit, 2), interrupts: make(chan string, 2)}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.RunCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.Session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.Agent = adapter.Engine{AgentEngine: engine}
	probe := &teamHITLQueueProbe{t: t, control: parentControl, routed: &routed, submit: teamQuestionSubmit(t, "submit")}
	o.EmitInputs = probe.emit

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(probe.asks) != 2 || probe.asks[0].TaskID == probe.asks[1].TaskID {
		t.Fatalf("expected one awaiting per member, got %#v", probe.asks)
	}
	for _, ask := range probe.asks {
		if ask.Mode != "question" || len(ask.Questions) != 1 || len(ask.Form) != 0 {
			t.Fatalf("member awaiting must keep its own mode and content: %#v", ask)
		}
	}
	seen := map[string]api.SubmitRequest{}
	for index := 0; index < 2; index++ {
		child := <-engine.submits
		seen[child.agentKey] = child.request
	}
	for _, key := range []string{"writer", "reviewer"} {
		request, ok := seen[key]
		if !ok || request.AwaitingID != "raw_await" || request.AgentKey != key || !strings.HasPrefix(request.SubmitID, "submit-") {
			t.Fatalf("unexpected child submit for %s: %#v", key, request)
		}
		items, decodeErr := api.DecodeSubmitParams(request.Params)
		if decodeErr != nil || len(items) != 1 || strings.TrimSpace(contracts.AnyStringNode(items[0]["answer"])) == "" {
			t.Fatalf("child params for %s were not forwarded unchanged: %#v err=%v", key, items, decodeErr)
		}
	}
	publicSubmits, publicAnswers := 0, 0
	for _, input := range routed {
		switch value := input.(type) {
		case stream.RequestSubmit:
			publicSubmits++
			if value.TaskID == "" || value.AwaitingID != value.TaskID+":raw_await" {
				t.Fatalf("member request.submit must use the public ID: %#v", value)
			}
		case stream.AwaitingAnswer:
			publicAnswers++
			if value.TaskID == "" || value.AwaitingID != value.TaskID+":raw_await" {
				t.Fatalf("member awaiting.answer must use the public ID: %#v", value)
			}
		}
	}
	if publicSubmits != 2 || publicAnswers != 2 {
		t.Fatalf("public HITL events submit=%d answer=%d routed=%#v", publicSubmits, publicAnswers, routed)
	}
	if parentControl.State() == contracts.RunLoopStateWaitingSubmit {
		t.Fatalf("Team run stayed in waiting_submit after the queue drained")
	}
}

func TestFrameOrchestratorTeamMemberFormKeepsViewAndSingleParam(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}},
	}}}
	defs := map[string]catalog.AgentDefinition{"writer": {Key: "writer", Name: "Writer", Mode: "REACT"}}
	ref := &view.Reference{Source: "connector", ConnectorID: "member-forms", Key: "edit", Hash: strings.Repeat("a", 64), Renderer: "html"}
	engine := &teamHITLTestEngine{submits: make(chan teamHITLChildSubmit, 1), interrupts: make(chan string, 1), formView: ref}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.RunCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.Session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 1}
	o.Agent = adapter.Engine{AgentEngine: engine}
	probe := &teamHITLQueueProbe{t: t, control: parentControl, routed: &routed, submit: func(stream.AwaitAsk) api.SubmitRequest {
		return api.SubmitRequest{SubmitID: "submit-form", Param: api.SubmitParam{"decision": "approve", "data": map[string]any{"name": "edited"}}}
	}}
	o.EmitInputs = probe.emit

	if failed, interrupted, err := o.Run(main); err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(probe.asks) != 1 {
		t.Fatalf("expected one member form awaiting, got %#v", probe.asks)
	}
	ask := probe.asks[0]
	if ask.Mode != "form" || ask.View == nil || ask.View.Hash != ref.Hash || ask.Form["title"] != "Edit writer" {
		t.Fatalf("member form lost its view or definition: %#v", ask)
	}
	child := <-engine.submits
	data, _ := child.request.Param["data"].(map[string]any)
	if child.request.Params != nil || child.request.Param["decision"] != "approve" || data["name"] != "edited" {
		t.Fatalf("member did not receive the single form param: %#v", child.request)
	}
}

func TestFrameOrchestratorTeamDelegationInterruptCancelsQueuedHITLChildren(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	engine := &teamHITLTestEngine{submits: make(chan teamHITLChildSubmit, 2), interrupts: make(chan string, 2)}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.RunCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.Session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.Agent = adapter.Engine{AgentEngine: engine}
	o.EmitInputs = func(inputs ...stream.StreamInput) {
		routed = append(routed, inputs...)
		for _, input := range inputs {
			if _, ok := input.(stream.AwaitAsk); ok {
				parentControl.Interrupt(contracts.InterruptInfo{Source: contracts.InterruptSourceHTTPAPI, Reason: contracts.InterruptReasonUserCancelled})
			}
		}
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := o.Run(main)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delegation interrupt returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delegation did not stop after Team interrupt")
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case key := <-engine.interrupts:
			seen[key] = true
		case <-time.After(time.Second):
			t.Fatalf("not all children observed interrupt: %#v", seen)
		}
	}
}

func TestFrameOrchestratorTeamCustomTaskDelegationQueuesMemberHITL(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks: []contracts.SubAgentTaskSpec{
			{SubAgentKey: "writer", TaskText: "draft", TaskName: "Draft"},
			{SubAgentKey: "reviewer", TaskText: "review", TaskName: "Review"},
		},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
	}
	engine := &teamHITLTestEngine{submits: make(chan teamHITLChildSubmit, 2), interrupts: make(chan string, 2)}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.RunCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.Session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.Agent = adapter.Engine{AgentEngine: engine}
	o.BuildQuerySession = func(_ context.Context, req runtimetypes.QueryCommand, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		if !options.IncludeHistory || options.AllowInvokeAgents || options.TeamHistoryAgentKey != def.Key {
			t.Fatalf("unexpected Team delegation options: %#v", options)
		}
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}
	probe := &teamHITLQueueProbe{t: t, control: parentControl, routed: &routed, submit: teamQuestionSubmit(t, "submit-invoke")}
	o.EmitInputs = probe.emit

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(probe.asks) != 2 || len(engine.submits) != 2 {
		t.Fatalf("delegation HITL was not queued per member: asks=%d submits=%d", len(probe.asks), len(engine.submits))
	}
	if len(main.injected) != 1 || main.injected[0].isError {
		t.Fatalf("delegation did not resume coordinator: injected=%#v", main.injected)
	}
}

func TestFrameOrchestratorTeamDelegationQueuesHITLAcrossBoundedParallelism(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}, {SubAgentKey: "analyst"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
		"analyst":  {Key: "analyst", Name: "Analyst", Mode: "REACT"},
	}
	engine := &teamHITLTestEngine{submits: make(chan teamHITLChildSubmit, 3), interrupts: make(chan string, 3)}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)
	snapshot := catalog.NewTeamSnapshot(catalog.TeamDefinition{
		TeamID: "research", Name: "Research", RuntimeMode: catalog.TeamRuntimeModeOrchestrated,
		AgentKeys:    []string{"writer", "reviewer", "analyst"},
		Orchestrator: catalog.TeamOrchestratorConfig{ModelKey: "mock-model", MaxParallel: 2},
	}, defs)
	o.TeamSnapshot = &snapshot
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.RunCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.Session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.Agent = adapter.Engine{AgentEngine: engine}
	probe := &teamHITLQueueProbe{t: t, control: parentControl, routed: &routed, submit: teamQuestionSubmit(t, "submit-wave")}
	o.EmitInputs = probe.emit

	done := make(chan error, 1)
	go func() {
		_, _, err := o.Run(main)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bounded delegation returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded delegation deadlocked while a waiting member held the semaphore")
	}
	if len(probe.asks) != 3 {
		t.Fatalf("published member awaitings=%d, want 3", len(probe.asks))
	}
	if len(engine.submits) != 3 {
		t.Fatalf("distributed child submits=%d, want 3", len(engine.submits))
	}
}

var _ contracts.AgentEngine = (*teamHITLTestEngine)(nil)
var _ contracts.OrchestratableAgentStream = (*teamHITLTestStream)(nil)
