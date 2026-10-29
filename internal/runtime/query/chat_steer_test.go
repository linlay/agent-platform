package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runops"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestChatSteerNativeModesAndLaterCaller(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		t.Run(mode, func(t *testing.T) {
			runs := runstate.NewManager()
			origin := &contracts.RunOrigin{AgentKey: "caller", Subject: "alice", ChatID: "original-chat", RunID: "original"}
			_, original, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "original", ChatID: "original-chat", AgentKey: "caller", RunOwner: contracts.AgentRunOwner("caller")})
			runs.Finish("original")
			if original.Context().Err() == nil {
				t.Fatal("original caller remains active")
			}
			_, target, _ := runs.Register(context.Background(), contracts.QuerySession{Mode: mode, RunID: "target", ChatID: "target-chat", AgentKey: "target-agent", RunOwner: contracts.AgentRunOwner("target-agent"), RunOrigin: origin, AccessLevel: "full_access"})
			_, later, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "later", ChatID: "later-chat", AgentKey: "caller", RunOwner: contracts.AgentRunOwner("caller"), AccessLevel: "ask_for_approval"})
			service := NewService(Dependencies{Runs: runs})
			handler := runops.NewToolHandler(service, runs)
			exec := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "later", ChatID: "later-chat", AgentKey: "caller", Subject: "alice", RunOwner: contracts.AgentRunOwner("caller")}, RunControl: later}
			var ids []string
			for _, call := range []string{"call-1", "call-2"} {
				exec.CurrentToolID = call
				result, err := handler.Invoke(context.Background(), "chat_steer", map[string]any{"runId": "target", "message": call}, exec)
				if err != nil || result.Error != "" || result.Structured["accepted"] != true {
					t.Fatalf("%#v %v", result, err)
				}
				ids = append(ids, result.Structured["steerId"].(string))
			}
			queue := target.DrainSteers()
			if len(queue) != 2 {
				t.Fatalf("queue %#v", queue)
			}
			for i, steer := range queue {
				if steer.RunID != "target" || steer.Message != []string{"call-1", "call-2"}[i] || steer.SteerID != ids[i] || steer.Origin == nil || steer.Origin.Type != "agent" || steer.Origin.AgentKey != "caller" || steer.Origin.ChatID != "later-chat" || steer.Origin.RunID != "later" || steer.Origin.ToolID != steer.Message {
					t.Fatalf("steer %#v source %#v", steer, steer.Origin)
				}
			}
			status, _ := runs.RunStatus("target")
			if status.AccessLevel != "full_access" {
				t.Fatal("steer changed target permission")
			}
			runs.Finish("target")
			exec.CurrentToolID = "ended"
			ended, _ := handler.Invoke(context.Background(), "chat_steer", map[string]any{"runId": "target", "message": "too late"}, exec)
			if ended.Structured["accepted"] != false {
				t.Fatal(ended.Output)
			}
			runs.Finish("later")
		})
	}
}

func TestSteerSourceCannotBeDecodedFromClientInput(t *testing.T) {
	var request queryinput.SteerRequest
	if err := json.Unmarshal([]byte(`{"runId":"target","agentKey":"agent","message":"change","origin":{"type":"agent","agentKey":"forged","chatId":"forged","runId":"forged","toolId":"forged"}}`), &request); !errors.Is(err, queryinput.ErrUnknownField) {
		t.Fatalf("client provenance must be rejected: %v", err)
	}
	if request.Origin != nil {
		t.Fatal("client supplied trusted provenance")
	}
	if err := json.Unmarshal([]byte(`{"runId":"target","agentKey":"agent","message":"change"}`), &request); err != nil {
		t.Fatal(err)
	}
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "chat", AgentKey: "agent", RunOwner: contracts.AgentRunOwner("agent")})
	service := NewService(Dependencies{Runs: runs})
	result, err := service.Steer(context.Background(), runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: request.RunID, AgentKey: request.AgentKey}, Message: request.Message})
	if err != nil || !result.Accepted {
		t.Fatalf("%#v %v", result, err)
	}
	if got := control.DrainSteers(); len(got) != 1 || got[0].Origin != nil || !reflect.DeepEqual(got[0].Message, request.Message) {
		t.Fatal(got)
	}
}

type failedSteerProxy struct {
	unavailableProxy
	calls   int
	request queryinput.SteerRequest
	err     *runtimetypes.RequestError
}

func (p *failedSteerProxy) Steer(req queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool) {
	p.calls++
	p.request = req
	return queryinput.SteerResponse{}, p.err, true
}

func TestChatSteerProxyErrorsPreserveOutcomeAndAreNotResent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         *runtimetypes.RequestError
		code, state string
	}{
		{"transport", &runtimetypes.RequestError{Status: 502, Message: "upstream timed out", OutcomeUnknown: true}, "run_steer_outcome_unknown", "unknown"},
		{"invalid_response", &runtimetypes.RequestError{Status: 502, Code: "proxy_invalid_response", Message: "invalid response", OutcomeUnknown: true}, "run_steer_outcome_unknown", "unknown"},
		{"not_queued", &runtimetypes.RequestError{Status: 502, Code: "run_steer_not_sent", Message: "not queued"}, "run_steer_not_sent", "not_started"},
		{"agent_mismatch", &runtimetypes.RequestError{Status: 403, Message: "agentKey does not match run"}, "run_steer_rejected", "not_started"},
		{"upstream_rejection", &runtimetypes.RequestError{Status: 502, Code: "proxy_control_failed", Message: "rejected"}, "proxy_control_failed", "not_started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := runstate.NewManager()
			_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "target-chat", AgentKey: "target-agent", RunOwner: contracts.AgentRunOwner("target-agent"), RunOrigin: &contracts.RunOrigin{AgentKey: "caller", Subject: "alice"}})
			defer runs.Finish("target")
			_, parent, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "caller", RunOwner: contracts.AgentRunOwner("caller")})
			defer runs.Finish("parent")
			proxy := &failedSteerProxy{err: tc.err}
			service := NewService(Dependencies{Runs: runs, Proxy: proxy})
			handler := runops.NewToolHandler(service, runs)
			exec := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "caller", Subject: "alice", RunOwner: contracts.AgentRunOwner("caller")}, RunControl: parent, CurrentToolID: "call"}
			args := map[string]any{"runId": "target", "message": "change"}
			first, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
			proxy.err = nil
			second, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
			if first.Error != tc.code || first.Structured["executionState"] != tc.state || !reflect.DeepEqual(first, second) || proxy.calls != 1 {
				t.Fatalf("%#v %#v calls=%d", first, second, proxy.calls)
			}
			if proxy.request.Origin == nil || proxy.request.Origin.RunID != "parent" || proxy.request.SteerID != first.Structured["steerId"] || len(control.DrainSteers()) != 0 {
				t.Fatalf("request %#v", proxy.request)
			}
			exec.CurrentToolID = "new-call"
			next, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
			if next.Error != "" || proxy.calls != 2 {
				t.Fatal("new invocation could not send after a definite rejection")
			}
			// Human controls keep the original transport status/code.
			proxy.err = tc.err
			_, err := service.Steer(context.Background(), runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: "target", AgentKey: "target-agent"}, Message: "human"})
			if err != tc.err {
				t.Fatalf("human error changed: %v", err)
			}
		})
	}
}

func TestChatSteerDoesNotAnswerOrdinaryHITL(t *testing.T) {
	for _, mode := range []string{"question", "approval", "form"} {
		t.Run(mode, func(t *testing.T) {
			runs := runstate.NewManager()
			_, target, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "target-chat", AgentKey: "target-agent", RunOwner: contracts.AgentRunOwner("target-agent"), RunOrigin: &contracts.RunOrigin{AgentKey: "caller", Subject: "alice"}})
			defer runs.Finish("target")
			session := contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "caller", Subject: "alice", RunOwner: contracts.AgentRunOwner("caller")}
			_, parent, _ := runs.Register(context.Background(), session)
			defer runs.Finish("parent")
			target.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "await", Mode: mode})
			handler := runops.NewToolHandler(NewService(Dependencies{Runs: runs}), runs)
			exec := &contracts.ExecutionContext{Session: session, RunControl: parent, CurrentToolID: "call"}
			result, _ := handler.Invoke(context.Background(), runops.SteerToolName, map[string]any{"runId": "target", "message": "change goal"}, exec)
			if result.Error != "" || result.Structured["accepted"] != true {
				t.Fatal(result.Output)
			}
			if awaiting, ok := target.LookupAwaiting("await"); !ok || awaiting.Mode != mode {
				t.Fatal("steer removed the human interaction")
			}
			if _, resolved := target.LookupResolvedSubmit("await"); resolved {
				t.Fatal("steer supplied a human response")
			}
			ack := target.ResolveSubmit(queryinput.SubmitRequest{RunID: "target", AwaitingID: "await", SubmitID: "human", Param: queryinput.SubmitParam{"confirmed": true}})
			if !ack.Accepted {
				t.Fatalf("human could not resolve interaction: %#v", ack)
			}
			queue := target.DrainSteers()
			if len(queue) != 1 || queue[0].Message != "change goal" || queue[0].SteerID != result.Structured["steerId"] {
				t.Fatalf("pending goal was lost: %#v", queue)
			}
		})
	}
}
