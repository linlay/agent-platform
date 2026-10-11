package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runops"
	runtimeproxy "agent-platform/internal/runtime/proxy"
)

func TestChatSteerChangesStartedRunAndPersistsCallerSource(t *testing.T) {
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	firstDone := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(firstDone) }) }
	t.Cleanup(release)
	secondMessages := make(chan []map[string]any, 1)
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"initial goal"}}]}`)
			close(firstStarted)
			select {
			case <-firstDone:
			case <-r.Context().Done():
				return
			}
			writeProviderSSE(t, w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			secondMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"revised goals handled"},"finish_reason":"stop"}]}`, `[DONE]`)
		default:
			t.Error("unexpected additional provider request")
		}
	})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	parent := registerTestParentRun(t, fixture, "parent")
	service := fixture.server.deps.Runtime.(runops.Runtime)
	handler := runops.NewToolHandler(service, fixture.runs)
	exec := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "zenmi", Subject: "alice", RunOwner: contracts.AgentRunOwner("zenmi")}, RunControl: parent, CurrentToolID: "start"}
	started, err := handler.Invoke(context.Background(), runops.StartToolName, map[string]any{"agentKey": "mock-agent", "message": "initial goal"}, exec)
	if err != nil || started.Error != "" {
		t.Fatalf("start: %#v %v", started, err)
	}
	run := started.Structured["run"].(map[string]any)
	runID, chatID := run["runId"].(string), run["chatId"].(string)
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("target did not start")
	}
	var ids []string
	for i, goal := range []string{"finish backend checks", "defer admin UI"} {
		if i == 1 {
			fixture.runs.Finish("parent")
			_, later, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "later", ChatID: "later-chat", AgentKey: "zenmi", Subject: "alice", RunOwner: contracts.AgentRunOwner("zenmi")})
			t.Cleanup(func() { fixture.runs.Finish("later") })
			exec = &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "later", ChatID: "later-chat", AgentKey: "zenmi", Subject: "alice", RunOwner: contracts.AgentRunOwner("zenmi")}, RunControl: later}
		}
		exec.CurrentToolID = goal
		args := map[string]any{"runId": runID, "message": goal}
		approval, err := handler.PrepareToolApproval(context.Background(), runops.SteerToolName, args, exec)
		if err != nil || approval != nil {
			t.Fatalf("steer unexpectedly needs approval: %#v %v", approval, err)
		}
		result, err := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
		if err != nil || result.Error != "" || result.Structured["accepted"] != true {
			t.Fatalf("steer: %#v %v", result, err)
		}
		ids = append(ids, result.Structured["steerId"].(string))
		replayed, _ := handler.Invoke(context.Background(), runops.SteerToolName, args, exec)
		if !reflect.DeepEqual(result, replayed) {
			t.Fatal("replay changed result")
		}
	}
	release()
	finished := waitRunTerminal(t, fixture.server, runID)
	if finished.Status != "completed" || !strings.Contains(finished.Content, "revised goals handled") {
		t.Fatalf("target did not complete revised work: %#v", finished)
	}
	select {
	case messages := <-secondMessages:
		var userGoals []string
		for _, message := range messages {
			if message["role"] == "user" {
				userGoals = append(userGoals, message["content"].(string))
			}
		}
		if !reflect.DeepEqual(userGoals, []string{"initial goal", "finish backend checks", "defer admin UI"}) {
			t.Fatalf("model input: %#v", messages)
		}
		encoded, _ := json.Marshal(messages)
		if strings.Contains(string(encoded), "parent-chat") || strings.Contains(string(encoded), `"source"`) {
			t.Fatalf("provenance leaked into model input: %s", encoded)
		}
	default:
		t.Fatal("second provider request missing")
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, event := range detail.Events {
		if event.Type != "request.steer" {
			continue
		}
		if count >= len(ids) || event.String("runId") != runID || event.String("steerId") != ids[count] || event.String("role") != "user" {
			t.Fatalf("unexpected event: %#v", event)
		}
		source, ok := event.Value("source").(map[string]any)
		wantRun := []string{"parent", "later"}[count]
		if !ok || source["type"] != "agent" || source["agentKey"] != "zenmi" || source["runId"] != wantRun || source["chatId"] != wantRun+"-chat" || source["toolId"] != event.String("message") {
			t.Fatalf("unexpected source: %#v", source)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("steer event count %d", count)
	}
}

func TestChatSteerProxyDoesNotForwardSource(t *testing.T) {
	fixture := newTestFixture(t)
	for _, transport := range []string{"ws", "sse"} {
		t.Run(transport, func(t *testing.T) {
			route := runtimeproxy.NewRoute("target-"+transport, "target-chat", "target-agent")
			route.Transport = transport
			var received map[string]any
			if transport == "sse" {
				upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accepted": true, "status": "accepted", "runId": route.RunID, "steerId": "id"}})
				}))
				defer upstream.Close()
				route.BaseURL = upstream.URL
			}
			fixture.server.registerProxyRun(route)
			defer fixture.server.unregisterProxyRun(route.RunID, route)
			source := &queryinput.SteerSource{Type: "agent", AgentKey: "caller", ChatID: "caller-chat", RunID: "caller-run", ToolID: "call"}
			result, statusErr, forwarded := fixture.server.forwardProxySteer(api.SteerRequest{RunID: route.RunID, AgentKey: route.AgentKey, SteerID: "id", Message: "goal", Source: source})
			if statusErr != nil || !forwarded || !result.Accepted {
				t.Fatalf("%#v %v %v", result, statusErr, forwarded)
			}
			if transport == "ws" {
				received = (<-route.SendQueue)["payload"].(map[string]any)
			}
			if received["source"] != nil || received["steerId"] != "id" || received["message"] != "goal" {
				t.Fatalf("upstream payload: %#v", received)
			}
		})
	}
}
