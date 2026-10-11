package query

import (
	"context"
	"encoding/json"
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
			_, later, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "later", ChatID: "later-chat", AgentKey: "caller", RunOwner: contracts.AgentRunOwner("caller"), AccessLevel: "default"})
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
				if steer.RunID != "target" || steer.Message != []string{"call-1", "call-2"}[i] || steer.SteerID != ids[i] || steer.Source == nil || steer.Source.Type != "agent" || steer.Source.AgentKey != "caller" || steer.Source.ChatID != "later-chat" || steer.Source.RunID != "later" || steer.Source.ToolID != steer.Message {
					t.Fatalf("steer %#v source %#v", steer, steer.Source)
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
	if err := json.Unmarshal([]byte(`{"runId":"target","agentKey":"agent","message":"change","source":{"type":"agent","agentKey":"forged","chatId":"forged","runId":"forged","toolId":"forged"}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Source != nil {
		t.Fatal("client supplied trusted provenance")
	}
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "chat", AgentKey: "agent", RunOwner: contracts.AgentRunOwner("agent")})
	service := NewService(Dependencies{Runs: runs})
	result, err := service.Steer(context.Background(), runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: request.RunID, AgentKey: request.AgentKey}, Message: request.Message})
	if err != nil || !result.Accepted {
		t.Fatalf("%#v %v", result, err)
	}
	if got := control.DrainSteers(); len(got) != 1 || got[0].Source != nil || !reflect.DeepEqual(got[0].Message, request.Message) {
		t.Fatal(got)
	}
}

type failedSteerProxy struct {
	unavailableProxy
	calls   int
	request queryinput.SteerRequest
}

func (p *failedSteerProxy) Steer(req queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool) {
	p.calls++
	p.request = req
	return queryinput.SteerResponse{}, &runtimetypes.RequestError{Status: 502, Message: "upstream timed out"}, true
}

func TestChatSteerProxyErrorIsUnknownAndNotResent(t *testing.T) {
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "target-chat", AgentKey: "target-agent", RunOwner: contracts.AgentRunOwner("target-agent"), RunOrigin: &contracts.RunOrigin{AgentKey: "caller", Subject: "alice"}})
	defer runs.Finish("target")
	proxy := &failedSteerProxy{}
	service := NewService(Dependencies{Runs: runs, Proxy: proxy})
	handler := runops.NewToolHandler(service, runs)
	exec := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "caller", Subject: "alice", RunOwner: contracts.AgentRunOwner("caller")}, CurrentToolID: "call"}
	args := map[string]any{"runId": "target", "message": "change"}
	first, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
	second, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
	if first.Error != "run_steer_outcome_unknown" || first.Structured["executionState"] != "unknown" || !reflect.DeepEqual(first, second) || proxy.calls != 1 {
		t.Fatalf("%#v %#v calls=%d", first, second, proxy.calls)
	}
	if proxy.request.Source == nil || proxy.request.Source.RunID != "parent" || proxy.request.SteerID != first.Structured["steerId"] || len(control.DrainSteers()) != 0 {
		t.Fatalf("request %#v", proxy.request)
	}
}
