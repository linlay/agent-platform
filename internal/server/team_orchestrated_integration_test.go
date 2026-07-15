package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestOrchestratedTeamDelegationEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var coordinatorChoices []string
	var coordinatorToolCounts []int
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		messages, _ := body["messages"].([]any)
		systemText := ""
		for index, raw := range messages {
			message, _ := raw.(map[string]any)
			role, _ := message["role"].(string)
			if index == 0 && role == "system" {
				systemText, _ = message["content"].(string)
			}
		}
		if strings.Contains(systemText, "hidden coordinator for a Team") {
			choice, _ := body["tool_choice"].(string)
			mu.Lock()
			coordinatorChoices = append(coordinatorChoices, choice)
			tools, _ := body["tools"].([]any)
			coordinatorToolCounts = append(coordinatorToolCounts, len(tools))
			coordinatorCall := len(coordinatorChoices)
			mu.Unlock()
			switch coordinatorCall {
			case 1:
				writeProviderSSE(t, w,
					providerToolCallFrame(t, "call-plan-add", contracts.PlanAddTasksToolName, map[string]any{"mode": "new", "tasks": []any{
						map[string]any{"description": "Collect member perspectives"},
					}}),
					`[DONE]`,
				)
				return
			case 2:
				writeProviderSSE(t, w,
					`{"choices":[{"delta":{"content":"invalid answer after planning only"},"finish_reason":"stop"}]}`,
					`[DONE]`,
				)
				return
			case 3:
				writeProviderSSE(t, w,
					`{"choices":[{"delta":{"reasoning_content":"choose every member"}}]}`,
					providerToolCallFrame(t, "call-agent-delegate", agentteam.ToolDelegate, map[string]any{"tasks": []any{
						map[string]any{"agentKey": "writer"},
						map[string]any{"agentKey": "reviewer"},
					}}),
					`[DONE]`,
				)
				return
			}
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"reasoning_content":"combine member replies"}}]}`,
				`{"choices":[{"delta":{"content":"Team summary"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
			return
		}
		answer := "unknown member"
		switch {
		case strings.Contains(systemText, "key: writer"):
			answer = "writer answer"
		case strings.Contains(systemText, "key: reviewer"):
			answer = "reviewer answer"
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"`+answer+`"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{setupRuntime: setupOrchestratedTeamRuntime(t)})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"chat-team-e2e","teamId":"research","message":"Give me every perspective"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	messages := decodeSSEMessages(t, rec.Body.String())
	requestEvent := findSSEMessageByType(t, messages, "request.query")
	if requestEvent["teamId"] != "research" {
		t.Fatalf("request.query owner=%#v", requestEvent)
	}
	if key, _ := requestEvent["agentKey"].(string); key != "" {
		t.Fatalf("request.query leaked coordinator key %q", key)
	}
	runStart := findSSEMessageByType(t, messages, "run.start")
	if _, present := runStart["ownerType"]; present || runStart["teamId"] != "research" {
		t.Fatalf("run.start owner=%#v", runStart)
	}

	memberReplies := map[string]string{}
	taskStarts := 0
	teamSummaryActor := false
	for _, message := range messages {
		typeName, _ := message["type"].(string)
		if typeName == "reasoning.delta" {
			t.Fatalf("hidden Team coordinator reasoning leaked to SSE: %#v", message)
		}
		if typeName == "tool.start" && (message["toolName"] == agentteam.ToolDelegate || message["toolName"] == "team_delegate" || message["toolName"] == "team_invoke") {
			t.Fatalf("hidden or legacy Team tool leaked to SSE: %#v", message)
		}
		if typeName == "task.start" {
			taskStarts++
			if message["presentation"] != "task" || message["teamId"] != "research" {
				t.Fatalf("unexpected member task metadata %#v", message)
			}
		}
		if typeName != "content.delta" {
			continue
		}
		key, _ := message["agentKey"].(string)
		delta, _ := message["delta"].(string)
		if message["presentation"] == "reply" && key == "" && delta == "Team summary" {
			actor, _ := message["actor"].(map[string]any)
			teamSummaryActor = actor["type"] == "team" && actor["teamId"] == "research" && message["teamId"] == "research"
		}
		if message["presentation"] == "task" && key != "" {
			memberReplies[key] += delta
		}
	}
	if taskStarts != 2 || memberReplies["writer"] != "writer answer" || memberReplies["reviewer"] != "reviewer answer" {
		t.Fatalf("tasks=%d replies=%#v events=%#v", taskStarts, memberReplies, messages)
	}
	if !strings.Contains(rec.Body.String(), `"delta":"Team summary"`) {
		t.Fatalf("missing Team summary: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "invalid answer after planning only") {
		t.Fatalf("invalid pre-routing coordinator text leaked to SSE: %s", rec.Body.String())
	}
	if !teamSummaryActor {
		t.Fatalf("Team summary did not carry the Team actor metadata: %#v", messages)
	}

	mu.Lock()
	choices := append([]string(nil), coordinatorChoices...)
	toolCounts := append([]int(nil), coordinatorToolCounts...)
	mu.Unlock()
	if len(choices) != 4 || len(toolCounts) != 4 {
		t.Fatalf("coordinator tool choices=%#v toolCounts=%#v, want four coordinator turns", choices, toolCounts)
	}
	for index := range choices {
		if choices[index] != "auto" || toolCounts[index] != 4 {
			t.Fatalf("coordinator tool choices=%#v toolCounts=%#v, want every upstream Team request to use auto with the full Team toolset", choices, toolCounts)
		}
	}

	summary, err := fixture.chats.Summary("chat-team-e2e")
	if err != nil || summary == nil {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	if !contracts.IsTeamRunOwner(summary.AgentKey, summary.TeamID) || summary.TeamID != "research" || summary.AgentKey != "" || summary.LastRunContent != "Team summary" {
		t.Fatalf("unexpected Team chat summary %#v", summary)
	}
	runs, err := fixture.chats.ListRuns("chat-team-e2e")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	if !contracts.IsTeamRunOwner(runs[0].AgentKey, runs[0].TeamID) || runs[0].TeamID != "research" || runs[0].AgentKey != "" || runs[0].AssistantText != "Team summary" {
		t.Fatalf("unexpected Team run %#v", runs[0])
	}
	jsonl, err := fixture.chats.LoadJSONLContent("chat-team-e2e")
	if err != nil {
		t.Fatalf("load Team JSONL: %v", err)
	}
	if strings.Contains(jsonl, hiddenTeamAgentKey("research")) {
		t.Fatalf("Team JSONL leaked the hidden coordinator key: %s", jsonl)
	}
	if strings.Contains(jsonl, `"name":"agent_invoke"`) {
		t.Fatalf("Team member system profile retained agent_invoke: %s", jsonl)
	}
	if strings.Contains(jsonl, `"name":"team_delegate"`) || strings.Contains(jsonl, `"name":"team_invoke"`) {
		t.Fatalf("new Team persistence exposed a legacy tool name: %s", jsonl)
	}
	detail, err := fixture.chats.LoadChat("chat-team-e2e")
	if err != nil {
		t.Fatalf("replay Team chat: %v", err)
	}
	replayedActors := map[string]string{}
	for _, event := range detail.Events {
		if event.Type == "tool.snapshot" && (event.String("toolName") == agentteam.ToolDelegate || event.String("toolName") == "team_delegate" || event.String("toolName") == "team_invoke") {
			t.Fatalf("replay exposed hidden Team tool: %#v", event)
		}
		if event.Type == "reasoning.snapshot" && event.String("taskId") == "" {
			t.Fatalf("replay exposed coordinator reasoning: %#v", event)
		}
		if event.Type != "content.snapshot" {
			continue
		}
		actor, _ := event.Payload["actor"].(map[string]any)
		replayedActors[event.String("text")] = strings.TrimSpace(contracts.AnyStringNode(actor["type"]))
	}
	if replayedActors["writer answer"] != "agent" || replayedActors["reviewer answer"] != "agent" || replayedActors["Team summary"] != "team" {
		t.Fatalf("replay actor metadata=%#v events=%#v", replayedActors, detail.Events)
	}
	hiddenHits, err := fixture.chats.SearchSession("chat-team-e2e", agentteam.ToolDelegate, 10)
	if err != nil || len(hiddenHits) != 0 {
		t.Fatalf("search exposed hidden coordinator records: hits=%#v err=%v", hiddenHits, err)
	}
	for _, legacyName := range []string{"team_delegate", "team_invoke"} {
		legacyHits, legacyErr := fixture.chats.SearchSession("chat-team-e2e", legacyName, 10)
		if legacyErr != nil || len(legacyHits) != 0 {
			t.Fatalf("search exposed legacy Team tool %q: hits=%#v err=%v", legacyName, legacyHits, legacyErr)
		}
	}
	visibleHits, err := fixture.chats.SearchSession("chat-team-e2e", "writer answer", 10)
	if err != nil || len(visibleHits) == 0 {
		t.Fatalf("search lost visible member answer: hits=%#v err=%v", visibleHits, err)
	}
}

func setupOrchestratedTeamRuntime(t *testing.T) func(string, *config.Config) {
	t.Helper()
	return func(_ string, cfg *config.Config) {
		for _, key := range []string{"writer", "reviewer"} {
			dir := filepath.Join(cfg.Paths.AgentsDir, key)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			content := strings.Join([]string{
				"key: " + key,
				"name: " + strings.Title(key),
				"mode: REACT",
				"visibility:",
				"  scopes:",
				"    - internal",
				"modelConfig:",
				"  modelKey: mock-model",
			}, "\n")
			if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		teamDir := filepath.Join(cfg.Paths.TeamsDir, "research")
		if err := os.MkdirAll(teamDir, 0o755); err != nil {
			t.Fatal(err)
		}
		teamYAML := strings.Join([]string{
			"name: Research",
			"description: Multi-agent research",
			"agentKeys:",
			"  - writer",
			"  - reviewer",
			"orchestrator:",
			"  modelConfig:",
			"    modelKey: mock-model",
			"  maxParallel: 2",
		}, "\n")
		if err := os.WriteFile(filepath.Join(teamDir, "team.yml"), []byte(teamYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
