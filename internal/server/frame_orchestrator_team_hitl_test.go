package server

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type teamHITLChildSubmit struct {
	agentKey string
	request  api.SubmitRequest
}

type teamHITLTestEngine struct {
	submits    chan teamHITLChildSubmit
	interrupts chan string
}

func (e *teamHITLTestEngine) Stream(ctx context.Context, req api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	return &teamHITLTestStream{
		ctx:        ctx,
		control:    contracts.RunControlFromContext(ctx),
		agentKey:   req.AgentKey,
		submits:    e.submits,
		interrupts: e.interrupts,
	}, nil
}

type teamHITLTestStream struct {
	ctx        context.Context
	control    *contracts.RunControl
	agentKey   string
	submits    chan teamHITLChildSubmit
	interrupts chan string
	step       int
}

func (s *teamHITLTestStream) Next() (contracts.AgentDelta, error) {
	switch s.step {
	case 0:
		s.step++
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
			Params:     result.Request.Params,
		}, nil
	case 2:
		s.step++
		return contracts.DeltaAwaitingAnswer{
			AwaitingID: "raw_await",
			Answer:     map[string]any{"mode": "question", "status": "answered"},
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

func TestFrameOrchestratorTeamFanoutMergesParallelHITLAndDistributesSubmit(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
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
	o.runCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.agent = engine
	mergedAskCount := 0
	o.emitInputs = func(inputs ...stream.StreamInput) {
		routed = append(routed, inputs...)
		for _, input := range inputs {
			ask, ok := input.(stream.AwaitAsk)
			if !ok {
				continue
			}
			mergedAskCount++
			if ask.Mode != "form" || len(ask.Forms) != 2 || ask.TaskID != "" {
				t.Fatalf("unexpected merged Team awaiting %#v", ask)
			}
			items := make([]map[string]any, 0, len(ask.Forms))
			for _, rawForm := range ask.Forms {
				form := contracts.AnyMapNode(rawForm)
				fieldID := strings.TrimSpace(contracts.AnyStringNode(form["id"]))
				if !strings.Contains(fieldID, ":raw_await") || strings.TrimSpace(contracts.AnyStringNode(form["taskId"])) == "" || contracts.AnyStringNode(form["awaitingId"]) != "raw_await" {
					t.Fatalf("merged field is not reversible: %#v", form)
				}
				items = append(items, map[string]any{
					"id":       fieldID,
					"decision": "approve",
					"form": map[string]any{
						"params": []any{map[string]any{"answer": "answer for " + contracts.AnyStringNode(form["taskId"])}},
					},
				})
			}
			params, err := api.EncodeSubmitParams(items)
			if err != nil {
				t.Fatalf("encode merged submit: %v", err)
			}
			ack := parentControl.ResolveSubmit(api.SubmitRequest{
				ChatID: "chat_1", RunID: "run_1", TeamID: "research", AwaitingID: ask.AwaitingID, SubmitID: "submit-team-1", Params: params,
			})
			if !ack.Accepted {
				t.Fatalf("merged submit not accepted: %#v", ack)
			}
		}
	}

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if mergedAskCount != 1 {
		t.Fatalf("merged awaiting count=%d, want 1", mergedAskCount)
	}
	seen := map[string]api.SubmitRequest{}
	for index := 0; index < 2; index++ {
		child := <-engine.submits
		seen[child.agentKey] = child.request
	}
	for _, key := range []string{"writer", "reviewer"} {
		request, ok := seen[key]
		if !ok || request.AwaitingID != "raw_await" || request.SubmitID != "submit-team-1" {
			t.Fatalf("unexpected child submit for %s: %#v", key, request)
		}
		items, decodeErr := api.DecodeSubmitParams(request.Params)
		if decodeErr != nil || len(items) != 1 || strings.TrimSpace(contracts.AnyStringNode(items[0]["answer"])) == "" {
			t.Fatalf("child params for %s are not reversible: %#v err=%v", key, items, decodeErr)
		}
	}
	publicAsks, publicSubmits, publicAnswers := 0, 0, 0
	for _, input := range routed {
		switch input.(type) {
		case stream.AwaitAsk:
			publicAsks++
		case stream.RequestSubmit:
			publicSubmits++
		case stream.AwaitingAnswer:
			publicAnswers++
		}
	}
	if publicAsks != 1 || publicSubmits != 1 || publicAnswers != 1 {
		t.Fatalf("public HITL events ask=%d submit=%d answer=%d routed=%#v", publicAsks, publicSubmits, publicAnswers, routed)
	}
}

func TestFrameOrchestratorTeamFanoutInterruptCancelsAllMergedHITLChildren(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
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
	o.runCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.agent = engine
	o.emitInputs = func(inputs ...stream.StreamInput) {
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
			t.Fatalf("fanout interrupt returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fanout did not stop after Team interrupt")
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

func TestFrameOrchestratorTeamInvokeMergesParallelHITL(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindInvoke,
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
	o.runCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.agent = engine
	o.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		if options.IncludeHistory || options.AllowInvokeAgents {
			t.Fatalf("unexpected Team invoke options: %#v", options)
		}
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}
	mergedAskCount := 0
	o.emitInputs = func(inputs ...stream.StreamInput) {
		routed = append(routed, inputs...)
		for _, input := range inputs {
			ask, ok := input.(stream.AwaitAsk)
			if !ok {
				continue
			}
			mergedAskCount++
			params := mergedTeamTestSubmitParams(t, ask)
			ack := parentControl.ResolveSubmit(api.SubmitRequest{
				ChatID: "chat_1", RunID: "run_1", TeamID: "research", AwaitingID: ask.AwaitingID, SubmitID: "submit-invoke-1", Params: params,
			})
			if !ack.Accepted {
				t.Fatalf("merged invoke submit not accepted: %#v", ack)
			}
		}
	}

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if mergedAskCount != 1 || len(engine.submits) != 2 {
		t.Fatalf("invoke HITL was not merged/distributed: asks=%d submits=%d", mergedAskCount, len(engine.submits))
	}
	if len(main.injected) != 1 || main.injected[0].isError || !main.optionalToolsAllowed {
		t.Fatalf("invoke did not resume coordinator: injected=%#v optional=%v", main.injected, main.optionalToolsAllowed)
	}
}

func TestFrameOrchestratorTeamFanoutMergesHITLInBoundedWaves(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}, {SubAgentKey: "analyst"}},
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
	o.teamSnapshot = &snapshot
	parentControl := contracts.NewRunControl(context.Background(), "run_1")
	o.runCtx = contracts.WithRunControl(parentControl.Context(), parentControl)
	o.session.TeamRuntime = &contracts.TeamRuntimeContext{RuntimeMode: catalog.TeamRuntimeModeOrchestrated, MaxParallel: 2}
	o.agent = engine
	mergedAskCount := 0
	o.emitInputs = func(inputs ...stream.StreamInput) {
		routed = append(routed, inputs...)
		for _, input := range inputs {
			ask, ok := input.(stream.AwaitAsk)
			if !ok {
				continue
			}
			mergedAskCount++
			params := mergedTeamTestSubmitParams(t, ask)
			ack := parentControl.ResolveSubmit(api.SubmitRequest{
				ChatID: "chat_1", RunID: "run_1", TeamID: "research", AwaitingID: ask.AwaitingID,
				SubmitID: "submit-wave", Params: params,
			})
			if !ack.Accepted {
				t.Fatalf("wave submit not accepted: %#v", ack)
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
			t.Fatalf("bounded fanout returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded fanout deadlocked while the first awaiting wave held the semaphore")
	}
	if mergedAskCount != 2 {
		t.Fatalf("merged wave count=%d, want 2", mergedAskCount)
	}
	if len(engine.submits) != 3 {
		t.Fatalf("distributed child submits=%d, want 3", len(engine.submits))
	}
}

func mergedTeamTestSubmitParams(t *testing.T, ask stream.AwaitAsk) api.SubmitParams {
	t.Helper()
	items := make([]map[string]any, 0, len(ask.Forms))
	for _, rawForm := range ask.Forms {
		form := contracts.AnyMapNode(rawForm)
		items = append(items, map[string]any{
			"id":       contracts.AnyStringNode(form["id"]),
			"decision": "approve",
			"form": map[string]any{
				"params": []any{map[string]any{"answer": "approved"}},
			},
		})
	}
	params, err := api.EncodeSubmitParams(items)
	if err != nil {
		t.Fatalf("encode merged Team submit: %v", err)
	}
	return params
}

var _ contracts.AgentEngine = (*teamHITLTestEngine)(nil)
var _ contracts.OrchestratableAgentStream = (*teamHITLTestStream)(nil)
