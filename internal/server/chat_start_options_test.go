package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runopspkg "agent-platform/internal/runops"
	"agent-platform/internal/runtime/controlscope"
)

func TestChatStartOptionalSettings(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "full_access"})
	defer fixture.runs.Finish("parent")
	for _, level := range []string{"", "default", "auto_approve", "full_access"} {
		req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "run message", AccessLevel: level, ChatName: "自定义名称 " + level, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}, References: queryinput.MakeReferences("skill", []string{"mock-skill"})}
		req.Input = map[string]any{"message": "run message", "chatName": req.ChatName, "references": []any{map[string]any{"type": "skill", "id": "mock-skill"}}}
		if level != "" {
			req.Input["accessLevel"] = level
		}
		started, err := fixture.server.StartRun(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		expected := level
		if expected == "" {
			expected = "default"
		}
		if started.AccessLevel != expected {
			t.Fatalf("started=%#v", started)
		}
		finished := waitRunTerminal(t, fixture.server, started.RunID)
		persisted, loadErr := fixture.chats.LoadRunQuery(started.ChatID, started.RunID)
		if loadErr != nil || persisted == nil {
			t.Fatalf("query not persisted: %v", loadErr)
		}
		// query is the chat_start arguments as the Agent wrote them; the
		// resolved selection and the caller sit beside it.
		if !reflect.DeepEqual(persisted.Query, req.Input) {
			t.Fatalf("chat_start input changed: %#v", persisted.Query)
		}
		raw, _ := json.Marshal(persisted.References)
		if !strings.Contains(string(raw), `"type":"skill"`) {
			t.Fatalf("skill selection lost: %s", raw)
		}
		if persisted.Origin == nil || persisted.Origin.Type != "agent" || persisted.Origin.RunID != "parent" || persisted.Origin.ToolID != "tool" {
			t.Fatalf("chat_start origin lost: %#v", persisted.Origin)
		}
		if finished.AccessLevel != expected {
			t.Fatalf("finished=%#v", finished)
		}
		summary, err := fixture.chats.Summary(started.ChatID)
		if err != nil || summary.ChatName != strings.TrimSpace(req.ChatName) {
			t.Fatalf("summary=%#v err=%v", summary, err)
		}
		if current, _ := parent.AccessLevelSnapshot(); current != "full_access" {
			t.Fatal("parent access level changed")
		}
		continued, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", ChatID: started.ChatID, Message: "continue", Origin: req.Origin})
		if err != nil {
			t.Fatal(err)
		}
		if continued.AccessLevel != "default" {
			t.Fatalf("continuation did not use target default: %#v", continued)
		}
		waitRunTerminal(t, fixture.server, continued.RunID)
		summary, _ = fixture.chats.Summary(started.ChatID)
		if summary.ChatName != strings.TrimSpace(req.ChatName) {
			t.Fatalf("name overwritten: %#v", summary)
		}
	}
}

func chatStartFixture(t *testing.T, parentLevel string) (testFixture, *contracts.RunControl) {
	t.Helper()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: parentLevel})
	t.Cleanup(func() { fixture.runs.Finish("parent") })
	return fixture, parent
}

func requireNoChatStarted(t *testing.T, fixture testFixture, err error, code string) *contracts.RunToolError {
	t.Helper()
	var typed *contracts.RunToolError
	if !errors.As(err, &typed) || typed.Code != code || typed.ExecutionState != "not_started" {
		t.Fatalf("want %s/not_started, got %#v", code, err)
	}
	chats, listErr := fixture.chats.ListChats("", "")
	if listErr != nil || len(chats) != 0 {
		t.Fatalf("a rejected start created chats: %#v %v", chats, listErr)
	}
	return typed
}

// Runtime itself refuses every start above the parent level that carries no
// consumable approval, whoever the caller is.
func TestChatStartPermissionMatrixWithoutApproval(t *testing.T) {
	levels := []string{"default", "auto_approve", "full_access"}
	for pi, parentLevel := range levels {
		for ri, requested := range levels {
			t.Run(parentLevel+"/"+requested, func(t *testing.T) {
				fixture, parent := chatStartFixture(t, parentLevel)
				req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: requested, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}}
				plan, err := fixture.server.PrepareRunStart(context.Background(), req)
				if ri > pi {
					requireNoChatStarted(t, fixture, err, "run_start_access_level_exceeded")
					_, err = fixture.server.StartRun(context.Background(), req)
					requireNoChatStarted(t, fixture, err, "run_start_access_level_exceeded")
					return
				}
				if err != nil || plan.RequiresApproval || plan.AccessLevel != requested {
					t.Fatalf("plan=%+v err=%v", plan, err)
				}
				started, err := fixture.server.StartRun(context.Background(), req)
				if err != nil || started.AccessLevel != requested {
					t.Fatalf("start=%+v err=%v", started, err)
				}
				waitRunTerminal(t, fixture.server, started.RunID)
				if level, _ := parent.AccessLevelSnapshot(); level != parentLevel {
					t.Fatal("changed parent")
				}
			})
		}
	}
}

func TestChatStartLegacyApprovalCannotOverrideCeiling(t *testing.T) {
	for _, parentLevel := range []string{"default", "auto_approve", "full_access"} {
		t.Run(parentLevel, func(t *testing.T) {
			fixture, _ := chatStartFixture(t, parentLevel)
			consumed := false
			req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: "full_access", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "legacy"}, Review: &contracts.RunStartReview{Consume: func(string) bool { consumed = true; return true }}}
			_, err := fixture.server.StartRun(context.Background(), req)
			code := "run_start_access_level_exceeded"
			if parentLevel == "full_access" {
				code = "run_start_review_stale"
			}
			requireNoChatStarted(t, fixture, err, code)
			if consumed {
				t.Fatal("consumed legacy permission escalation receipt")
			}
		})
	}
	fixture, _ := chatStartFixture(t, "full_access")
	req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.server.StartRun(ctx, req)
	requireNoChatStarted(t, fixture, err, "chat_start_cancelled")
	fixture.runs.Finish("parent")
	_, err = fixture.server.StartRun(context.Background(), req)
	requireNoChatStarted(t, fixture, err, "run_parent_not_active")
}

func TestChatStartTargetAdmissionForOptions(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\ninteractionConfig:\n  model: false\n  accessLevel: false\n  attachment:\n    skills: false\n")...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		},
	})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, _, _ = fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "full_access",
	})
	defer fixture.runs.Finish("parent")
	for _, tc := range []struct {
		request contracts.RunStartRequest
		code    string
	}{
		// The target's interactionConfig does not gate chat_start, but real
		// availability and request validation still do.
		{contracts.RunStartRequest{AgentKey: "mock-agent", ModelKey: "missing-model"}, "invalid_request"},
		{contracts.RunStartRequest{AgentKey: "mock-agent", References: queryinput.MakeReferences("skill", []string{"missing-skill"})}, "skill_reference_unavailable"},
		{contracts.RunStartRequest{AgentKey: "mock-agent", ChatID: "existing", ChatName: "name"}, "invalid_request"},
	} {
		tc.request.Message = "test"
		tc.request.Origin = contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}
		_, err := fixture.server.StartRun(context.Background(), tc.request)
		var typed *contracts.RunToolError
		if !errors.As(err, &typed) || typed.Code != tc.code {
			t.Fatalf("request=%#v err=%v", tc.request, err)
		}
	}
	chats, listErr := fixture.chats.ListChats("", "")
	if listErr != nil || len(chats) != 0 {
		t.Fatalf("rejected options created chats: %#v %v", chats, listErr)
	}
	for i, tc := range []struct {
		request contracts.RunStartRequest
		level   string
	}{
		{contracts.RunStartRequest{AccessLevel: "default"}, "default"},
		{contracts.RunStartRequest{}, "default"},
		{contracts.RunStartRequest{References: queryinput.MakeReferences("skill", []string{"mock-skill"})}, "default"},
		{contracts.RunStartRequest{AccessLevel: "auto_approve", ModelKey: "mock-model", ReasoningEffort: "LOW"}, "auto_approve"},
	} {
		tc.request.AgentKey, tc.request.Message = "mock-agent", "test"
		tc.request.Origin = contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: fmt.Sprintf("ok-%d", i)}
		started, err := fixture.server.StartRun(context.Background(), tc.request)
		if err != nil || started.AccessLevel != tc.level {
			t.Fatalf("request=%#v started=%#v err=%v", tc.request, started, err)
		}
		waitRunTerminal(t, fixture.server, started.RunID)
		// The frozen snapshot keeps gating what a user does to this Run later.
		if config, found := fixture.runs.RunInteractionConfig(started.RunID); found && (config.Model || config.AccessLevel || config.Attachment.Skills) {
			t.Fatalf("start relaxed the frozen interaction policy: %+v", config)
		}
	}

}

func TestChatStartDefaultDoesNotInheritLiveParentAccessLevel(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "default",
	})
	defer fixture.runs.Finish("parent")
	var chatID string
	for _, level := range []string{"default", "auto_approve", "full_access", "default"} {
		parent.UpdateAccessLevel(level)
		started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
			AgentKey: "mock-agent", ChatID: chatID, Message: "inherit current permission",
			Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if started.AccessLevel != "default" {
			t.Fatalf("want %s, got %#v", level, started)
		}
		parent.UpdateAccessLevel("auto_approve")
		finished := waitRunTerminal(t, fixture.server, started.RunID)
		if finished.AccessLevel != "default" {
			t.Fatalf("child permission changed with parent: %#v", finished)
		}
		chatID = started.ChatID
	}
}

// The real handler and the real Runtime together: a review that was shown is
// the only way an escalating call can start, and it is bound to its baseline.
func TestChatStartEscalationThroughHandler(t *testing.T) {
	fixture, parent := chatStartFixture(t, "auto_approve")
	handler := runopspkg.NewToolHandler(fixture.server.deps.Runtime.(runopspkg.Runtime), fixture.runs)
	e := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent")}, RunControl: parent, CurrentToolID: "escalate", CurrentToolName: runopspkg.StartToolName}
	args := map[string]any{"agentKey": "mock-agent", "message": "task", "accessLevel": "full_access"}
	approval, err := handler.PrepareToolApproval(context.Background(), runopspkg.StartToolName, args, e)
	if err != nil || approval != nil {
		t.Fatalf("explicit escalation must not offer review: %+v %v", approval, err)
	}
	e.ToolApprovals = map[string]bool{"forged": true}
	result, err := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e)
	if err != nil || result.Error != "run_start_access_level_exceeded" || result.Structured["executionState"] != "not_started" {
		t.Fatalf("%+v %v", result, err)
	}
	parent.UpdateAccessLevel("full_access")
	e.CurrentToolID = "after-user-raise"
	result, err = handler.Invoke(context.Background(), runopspkg.StartToolName, args, e)
	if err != nil || result.Error != "" {
		t.Fatalf("user raised permission: %+v %v", result, err)
	}
	run := result.Structured["run"].(map[string]any)
	waitRunTerminal(t, fixture.server, run["runId"].(string))
	retry, _ := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e)
	if retry.Structured["run"].(map[string]any)["runId"] != run["runId"] {
		t.Fatal("idempotent start created another Run")
	}
}

func TestChatStartParentLoweredBeforeAcceptance(t *testing.T) {
	fixture, parent := chatStartFixture(t, "full_access")
	req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: "full_access", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent"}}
	if _, err := fixture.server.PrepareRunStart(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	parent.UpdateAccessLevel("default")
	_, err := fixture.server.StartRun(context.Background(), req)
	requireNoChatStarted(t, fixture, err, "run_start_access_level_exceeded")
}

// narrowRunManager hides the atomic acceptance point of the real manager.
type narrowRunManager struct{ contracts.RunManager }

// Control identities are unrelated to the acceptance point and stay visible.
func (m narrowRunManager) BindRunControlScope(runID string, scope controlscope.Scope) controlscope.Scope {
	return m.RunManager.(controlscope.Live).BindRunControlScope(runID, scope)
}
func (m narrowRunManager) RunControlScope(runID string) (controlscope.Scope, bool) {
	return m.RunManager.(controlscope.Live).RunControlScope(runID)
}

type chatStartSwitchRegistry struct {
	catalog.Registry
	parent *contracts.RunControl
	calls  int
}

func (r *chatStartSwitchRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := r.Registry.AgentDefinition(key)
	if r.calls++; r.calls == 2 {
		r.parent.UpdateAccessLevel("full_access")
	}
	return def, ok
}

// The audit records the baseline verified at the acceptance point, not the
// one read when the request was prepared.
func TestChatStartAuditUsesAcceptedParentBaseline(t *testing.T) {
	fixture, parent := chatStartFixture(t, "auto_approve")
	fixture.server.deps.Registry = &chatStartSwitchRegistry{Registry: fixture.server.deps.Registry, parent: parent}
	bindTestRuntime(fixture.server)
	started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: "default", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "audit-call"}})
	if err != nil {
		t.Fatal(err)
	}
	waitRunTerminal(t, fixture.server, started.RunID)
	persisted, err := fixture.chats.LoadRunQuery(started.ChatID, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(persisted.AccessGrant)
	var audit map[string]any
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	level, version := parent.AccessLevelSnapshot()
	if level != "full_access" || audit["parentAccessLevel"] != level || audit["parentAccessVersion"] != float64(version) || audit["accessLevel"] != "default" {
		t.Fatalf("accepted parent=%s/v%d, persisted=%v", level, version, audit)
	}
}

// narrowRunManager hides the atomic acceptance point of the real manager.

// Control identities are unrelated to the acceptance point and stay visible.

func TestChatStartRefusesRunManagerWithoutAtomicAcceptance(t *testing.T) {
	fixture, _ := chatStartFixture(t, "auto_approve")
	fixture.server.deps.Runs = narrowRunManager{fixture.runs}
	bindTestRuntime(fixture.server)
	for _, level := range []string{"", "default", "auto_approve"} {
		_, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: level, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "narrow-" + level}})
		requireNoChatStarted(t, fixture, err, "run_start_authorization_unavailable")
	}
}

func TestChatStartDefaultAgentStartAndContinuation(t *testing.T) {
	fixture, _ := chatStartFixture(t, "full_access")
	req := contracts.RunStartRequest{Message: "task", ChatName: "named", AccessLevel: "full_access", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "default-target"}}
	plan, err := fixture.server.PrepareRunStart(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	explicit := req
	explicit.AgentKey = "mock-agent"
	other, err := fixture.server.PrepareRunStart(context.Background(), explicit)
	if err != nil || plan.RequestDigest != other.RequestDigest {
		t.Fatal("implicit target changed request identity")
	}
	started, err := fixture.server.StartRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitRunTerminal(t, fixture.server, started.RunID)
	continued, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{Message: "continue", ChatID: started.ChatID, Origin: req.Origin})
	if err != nil || continued.ChatID != started.ChatID || continued.AgentKey != "mock-agent" || continued.AccessLevel != "default" {
		t.Fatalf("%+v %v", continued, err)
	}
	waitRunTerminal(t, fixture.server, continued.RunID)
}
