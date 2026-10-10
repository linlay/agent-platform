package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runopspkg "agent-platform/internal/runops"
)

func TestChatStartOptionalSettings(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "full_access"})
	defer fixture.runs.Finish("parent")
	for _, level := range []string{"", "default", "auto_approve", "full_access"} {
		req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "run message", AccessLevel: level, MustUseSkills: []string{"mock-skill"}, ChatName: "自定义名称 " + level, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}}
		started, err := fixture.server.StartRun(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		expected := level
		if expected == "" {
			expected = "full_access"
		}
		if started.AccessLevel != expected {
			t.Fatalf("started=%#v", started)
		}
		finished := waitRunTerminal(t, fixture.server, started.RunID)
		persisted, loadErr := fixture.chats.LoadRunQuery(started.ChatID, started.RunID)
		if loadErr != nil || persisted == nil {
			t.Fatalf("query not persisted: %v", loadErr)
		}
		raw, _ := json.Marshal(persisted.Query)
		if !strings.Contains(string(raw), `"mustUseSkills":["mock-skill"]`) {
			t.Fatalf("skill selection lost: %s", raw)
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
		if continued.AccessLevel != "full_access" {
			t.Fatalf("continuation did not inherit parent level: %#v", continued)
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
	for parentRank, parentLevel := range levels {
		for requestedRank, requested := range levels {
			t.Run(parentLevel+"/"+requested, func(t *testing.T) {
				fixture, parent := chatStartFixture(t, parentLevel)
				req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: requested, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}}
				plan, err := fixture.server.PrepareRunStart(context.Background(), req)
				if err != nil || plan.RequiresApproval != (requestedRank > parentRank) || plan.AccessLevel != requested || plan.ParentAccessLevel != parentLevel {
					t.Fatalf("plan=%#v err=%v", plan, err)
				}
				if chats, _ := fixture.chats.ListChats("", ""); len(chats) != 0 {
					t.Fatal("preparing a start created a chat")
				}
				started, err := fixture.server.StartRun(context.Background(), req)
				if requestedRank > parentRank {
					if typed := requireNoChatStarted(t, fixture, err, "run_start_approval_required"); !typed.Retryable {
						t.Fatal("a missing review must be retryable so the caller can request one")
					}
					return
				}
				if err != nil || started.AccessLevel != requested {
					t.Fatalf("started=%#v err=%v", started, err)
				}
				waitRunTerminal(t, fixture.server, started.RunID)
				if current, _ := parent.AccessLevelSnapshot(); current != parentLevel {
					t.Fatal("starting a run changed the parent level")
				}
			})
		}
	}
}

func TestChatStartApprovalIsValidatedAndConsumedByRuntime(t *testing.T) {
	origin := contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}
	base := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: "full_access", Origin: origin}
	reviewed := func(t *testing.T, fixture testFixture, req contracts.RunStartRequest, consumed *[]string, grant bool) contracts.RunStartRequest {
		t.Helper()
		plan, err := fixture.server.PrepareRunStart(context.Background(), req)
		if err != nil || !plan.RequiresApproval {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
		req.Review = &contracts.RunStartReview{
			ParentAccessLevel: plan.ParentAccessLevel, ParentAccessVersion: plan.ParentAccessVersion, ApprovalDigest: plan.ApprovalDigest,
			Consume: func(digest string) bool {
				*consumed = append(*consumed, digest)
				return grant && digest == plan.ApprovalDigest && len(*consumed) == 1
			},
		}
		return req
	}
	t.Run("approved start consumes exactly one receipt", func(t *testing.T) {
		fixture, parent := chatStartFixture(t, "auto_approve")
		var consumed []string
		req := reviewed(t, fixture, base, &consumed, true)
		started, err := fixture.server.StartRun(context.Background(), req)
		if err != nil || started.AccessLevel != "full_access" || len(consumed) != 1 {
			t.Fatalf("started=%#v err=%v consumed=%v", started, err, consumed)
		}
		waitRunTerminal(t, fixture.server, started.RunID)
		persisted, loadErr := fixture.chats.LoadRunQuery(started.ChatID, started.RunID)
		raw, _ := json.Marshal(persisted)
		if loadErr != nil || !strings.Contains(string(raw), `"source":"approved"`) || !strings.Contains(string(raw), `"parentAccessLevel":"auto_approve"`) {
			t.Fatalf("permission audit missing: %s %v", raw, loadErr)
		}
		if current, _ := parent.AccessLevelSnapshot(); current != "auto_approve" {
			t.Fatal("approval elevated the parent run")
		}
		// The spent receipt cannot start a second run.
		if _, err := fixture.server.StartRun(context.Background(), req); err == nil {
			t.Fatal("receipt was reused")
		}
	})
	t.Run("missing receipt", func(t *testing.T) {
		fixture, _ := chatStartFixture(t, "auto_approve")
		var consumed []string
		_, err := fixture.server.StartRun(context.Background(), reviewed(t, fixture, base, &consumed, false))
		if typed := requireNoChatStarted(t, fixture, err, "run_start_approval_required"); typed.Retryable {
			t.Fatal("a reviewed call without a receipt must not be retried automatically")
		}
		req := reviewed(t, fixture, base, &consumed, true)
		req.Review.Consume = nil
		_, err = fixture.server.StartRun(context.Background(), req)
		requireNoChatStarted(t, fixture, err, "run_start_approval_required")
	})
	t.Run("raising the parent does not bypass a shown review", func(t *testing.T) {
		fixture, parent := chatStartFixture(t, "auto_approve")
		var consumed []string
		req := reviewed(t, fixture, base, &consumed, true)
		parent.UpdateAccessLevel("full_access")
		_, err := fixture.server.StartRun(context.Background(), req)
		if typed := requireNoChatStarted(t, fixture, err, "run_start_review_stale"); !typed.Retryable || len(consumed) != 0 {
			t.Fatalf("stale review consumed a receipt: %#v %v", typed, consumed)
		}
	})
	t.Run("baseline version is strict even when the level returns", func(t *testing.T) {
		fixture, parent := chatStartFixture(t, "auto_approve")
		var consumed []string
		req := reviewed(t, fixture, base, &consumed, true)
		parent.UpdateAccessLevel("default")
		parent.UpdateAccessLevel("auto_approve")
		_, err := fixture.server.StartRun(context.Background(), req)
		requireNoChatStarted(t, fixture, err, "run_start_review_stale")
		if len(consumed) != 0 {
			t.Fatal("stale review consumed a receipt")
		}
	})
	t.Run("approval is bound to the reviewed request", func(t *testing.T) {
		fixture, _ := chatStartFixture(t, "auto_approve")
		var consumed []string
		req := reviewed(t, fixture, base, &consumed, true)
		req.Message = "a different task"
		_, err := fixture.server.StartRun(context.Background(), req)
		requireNoChatStarted(t, fixture, err, "run_start_review_stale")
		if len(consumed) != 0 {
			t.Fatal("receipt consumed for a different request")
		}
	})
	t.Run("lowered parent requires a review that was never shown", func(t *testing.T) {
		fixture, parent := chatStartFixture(t, "full_access")
		parent.UpdateAccessLevel("default")
		_, err := fixture.server.StartRun(context.Background(), base)
		requireNoChatStarted(t, fixture, err, "run_start_approval_required")
	})
	t.Run("cancelled caller and finished parent", func(t *testing.T) {
		fixture, _ := chatStartFixture(t, "full_access")
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fixture.server.StartRun(cancelled, base)
		requireNoChatStarted(t, fixture, err, "chat_start_cancelled")
		fixture.runs.Finish("parent")
		_, err = fixture.server.StartRun(context.Background(), base)
		requireNoChatStarted(t, fixture, err, "run_parent_not_active")
	})
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
			data = append(data, []byte("\ninteractionConfig:\n  accessLevel: false\n  mustUseSkills: false\n")...)
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
		{contracts.RunStartRequest{AgentKey: "mock-agent"}, "interaction_disabled"},
		{contracts.RunStartRequest{AgentKey: "mock-agent", AccessLevel: "full_access"}, "interaction_disabled"},
		{contracts.RunStartRequest{AgentKey: "mock-agent", MustUseSkills: []string{"demo"}}, "interaction_disabled"},
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
	started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		AgentKey: "mock-agent", Message: "explicit default remains allowed", AccessLevel: "default", MustUseSkills: []string{},
		Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "default"},
	})
	if err != nil || started.AccessLevel != "default" {
		t.Fatalf("default rejected: %#v %v", started, err)
	}
	waitRunTerminal(t, fixture.server, started.RunID)

}

func TestChatStartInheritsLiveParentAccessLevel(t *testing.T) {
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
		if started.AccessLevel != level {
			t.Fatalf("want %s, got %#v", level, started)
		}
		parent.UpdateAccessLevel("auto_approve")
		finished := waitRunTerminal(t, fixture.server, started.RunID)
		if finished.AccessLevel != level {
			t.Fatalf("child permission changed with parent: %#v", finished)
		}
		chatID = started.ChatID
	}
}

// The real handler and the real Runtime together: a review that was shown is
// the only way an escalating call can start, and it is bound to its baseline.
func TestChatStartEscalationThroughHandler(t *testing.T) {
	fixture, parent := chatStartFixture(t, "auto_approve")
	runtimeService, ok := fixture.server.deps.Runtime.(runopspkg.Runtime)
	if !ok {
		t.Fatal("assembled runtime does not expose the run-tool surface")
	}
	handler := runopspkg.NewToolHandler(runtimeService, fixture.runs)
	exec := func(toolID string) *contracts.ExecutionContext {
		return &contracts.ExecutionContext{
			Session:    contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent")},
			RunControl: parent, CurrentToolID: toolID, CurrentToolName: runopspkg.StartToolName,
		}
	}
	args := map[string]any{"agentKey": "mock-agent", "message": "task", "accessLevel": "full_access"}
	chatCount := func() int {
		chats, err := fixture.chats.ListChats("", "")
		if err != nil {
			t.Fatal(err)
		}
		return len(chats)
	}
	review := func(e *contracts.ExecutionContext) *contracts.ToolApproval {
		t.Helper()
		approval, err := handler.PrepareToolApproval(context.Background(), runopspkg.StartToolName, args, e)
		if err != nil {
			t.Fatal(err)
		}
		return approval
	}
	invoke := func(e *contracts.ExecutionContext) contracts.ToolExecutionResult {
		t.Helper()
		result, err := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e)
		if err != nil {
			t.Fatal(err)
		}
		// Let started runs finish before the fixture directories are removed.
		if run, ok := result.Structured["run"].(map[string]any); ok && result.Error == "" {
			waitRunTerminal(t, fixture.server, run["runId"].(string))
		}
		return result
	}

	// Shown but never approved: nothing starts.
	e := exec("unapproved")
	if approval := review(e); approval == nil || approval.AllowAutoApprove {
		t.Fatalf("escalation must require manual review: %#v", approval)
	}
	if result := invoke(e); result.Error != "run_start_approval_required" || result.Structured["executionState"] != "not_started" || chatCount() != 0 {
		t.Fatalf("unapproved start: %#v chats=%d", result, chatCount())
	}

	// The user raises the parent while the review is displayed, then approves.
	e = exec("raised")
	approval := review(e)
	parent.UpdateAccessLevel("full_access")
	e.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	if result := invoke(e); result.Error != "run_start_review_stale" || result.Structured["retryable"] != true || chatCount() != 0 {
		t.Fatalf("stale review: %#v chats=%d", result, chatCount())
	}
	// A new call is judged against the new parent level and needs no review.
	e = exec("after-raise")
	if approval := review(e); approval != nil {
		t.Fatalf("no escalation remains: %#v", approval)
	}
	if result := invoke(e); result.Error != "" || chatCount() != 1 {
		t.Fatalf("direct start: %#v", result)
	}

	// Approved against an unchanged baseline: exactly one run, receipt spent.
	parent.UpdateAccessLevel("auto_approve")
	e = exec("approved")
	approval = review(e)
	e.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	result := invoke(e)
	run, _ := result.Structured["run"].(map[string]any)
	if result.Error != "" || run["accessLevel"] != "full_access" || len(e.ToolApprovals) != 0 || chatCount() != 2 {
		t.Fatalf("approved start: %#v approvals=%v chats=%d", result, e.ToolApprovals, chatCount())
	}
	if level, _ := parent.AccessLevelSnapshot(); level != "auto_approve" {
		t.Fatal("approval elevated the parent run")
	}
	// Retrying the same call returns the same run without a new approval.
	retry := invoke(e)
	retried, _ := retry.Structured["run"].(map[string]any)
	if retry.Error != "" || retried["runId"] != run["runId"] || chatCount() != 2 {
		t.Fatalf("retry: %#v", retry)
	}
	if again := review(e); again != nil {
		t.Fatal("a started call asked for review again")
	}

	// Judged as no escalation, then the parent is lowered before execution.
	parent.UpdateAccessLevel("full_access")
	e = exec("lowered")
	if approval := review(e); approval != nil {
		t.Fatalf("unexpected review: %#v", approval)
	}
	parent.UpdateAccessLevel("default")
	if result := invoke(e); result.Error != "run_start_approval_required" || result.Structured["retryable"] != true || chatCount() != 2 {
		t.Fatalf("lowered parent: %#v", result)
	}
}

// A reviewed call keeps its frozen baseline: replaying the same tool call can
// never start without the receipt, even once the request no longer escalates.
func TestChatStartReviewedCallCannotBeReplayedWithoutReceipt(t *testing.T) {
	fixture, parent := chatStartFixture(t, "auto_approve")
	handler := runopspkg.NewToolHandler(fixture.server.deps.Runtime.(runopspkg.Runtime), fixture.runs)
	e := &contracts.ExecutionContext{
		Session:    contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent")},
		RunControl: parent, CurrentToolID: "same-call", CurrentToolName: runopspkg.StartToolName,
	}
	args := map[string]any{"agentKey": "mock-agent", "message": "task", "accessLevel": "full_access"}
	approval, err := handler.PrepareToolApproval(context.Background(), runopspkg.StartToolName, args, e)
	if err != nil || approval == nil {
		t.Fatalf("review missing: %v", err)
	}
	parent.UpdateAccessLevel("full_access")
	e.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	if first, _ := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e); first.Error != "run_start_review_stale" {
		t.Fatalf("first=%#v", first)
	}
	// The LLM layer drops the temporary receipt when the invocation returns.
	e.ToolApprovals = nil
	for range 2 {
		replay, _ := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e)
		if replay.Error != "run_start_review_stale" {
			t.Fatalf("replayed stale call: %#v", replay)
		}
	}
	// Re-planning the same call shows the frozen review, not a fresh judgment.
	again, err := handler.PrepareToolApproval(context.Background(), runopspkg.StartToolName, args, e)
	if err != nil || again == nil || again.Fingerprint != approval.Fingerprint || again.AllowAutoApprove {
		t.Fatalf("reviewed call was re-planned: %#v %v", again, err)
	}
	e.ToolApprovals = map[string]bool{again.Fingerprint: true}
	if replay, _ := handler.Invoke(context.Background(), runopspkg.StartToolName, args, e); replay.Error != "run_start_review_stale" {
		t.Fatalf("re-approved stale call: %#v", replay)
	}
	if chats, _ := fixture.chats.ListChats("", ""); len(chats) != 0 {
		t.Fatalf("stale call created chats: %#v", chats)
	}
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
	raw, _ := json.Marshal(persisted.Query)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	audit := data["runOrigin"].(map[string]any)["permission"].(map[string]any)
	level, version := parent.AccessLevelSnapshot()
	if level != "full_access" || audit["parentAccessLevel"] != level || audit["parentAccessVersion"] != float64(version) || audit["accessLevel"] != "default" {
		t.Fatalf("accepted parent=%s/v%d, persisted=%v", level, version, audit)
	}
}

// narrowRunManager hides the atomic acceptance point of the real manager.
type narrowRunManager struct{ contracts.RunManager }

func TestChatStartRefusesRunManagerWithoutAtomicAcceptance(t *testing.T) {
	fixture, _ := chatStartFixture(t, "auto_approve")
	fixture.server.deps.Runs = narrowRunManager{fixture.runs}
	bindTestRuntime(fixture.server)
	for _, level := range []string{"", "default", "auto_approve"} {
		_, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", AccessLevel: level, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "narrow-" + level}})
		requireNoChatStarted(t, fixture, err, "run_start_authorization_unavailable")
	}
}

func TestChatStartDefaultAgentReviewStartAndContinuation(t *testing.T) {
	fixture, _ := chatStartFixture(t, "default")
	req := contracts.RunStartRequest{
		Message: "create coding agents", ChatName: "冒烟-A1-创建编程智能体", AccessLevel: "full_access",
		Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "default-target"},
	}
	plan, err := fixture.server.PrepareRunStart(context.Background(), req)
	if err != nil || !plan.RequiresApproval {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	explicit := req
	explicit.AgentKey = "mock-agent"
	explicitPlan, err := fixture.server.PrepareRunStart(context.Background(), explicit)
	if err != nil || explicitPlan.RequestDigest != plan.RequestDigest || explicitPlan.ApprovalDigest != plan.ApprovalDigest {
		t.Fatalf("default and explicit targets differ: %#v %#v %v", plan, explicitPlan, err)
	}
	_, err = fixture.server.StartRun(context.Background(), req)
	requireNoChatStarted(t, fixture, err, "run_start_approval_required")
	consumed := 0
	req.Review = &contracts.RunStartReview{
		ParentAccessLevel: plan.ParentAccessLevel, ParentAccessVersion: plan.ParentAccessVersion, ApprovalDigest: plan.ApprovalDigest,
		Consume: func(digest string) bool { consumed++; return consumed == 1 && digest == plan.ApprovalDigest },
	}
	started, err := fixture.server.StartRun(context.Background(), req)
	if err != nil || started.AgentKey != "mock-agent" || consumed != 1 {
		t.Fatalf("start=%#v err=%v consumed=%d", started, err, consumed)
	}
	waitRunTerminal(t, fixture.server, started.RunID)
	summary, err := fixture.chats.Summary(started.ChatID)
	if err != nil || summary.ChatName != req.ChatName || summary.AgentKey != "mock-agent" {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	continued, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		Message: "continue", ChatID: started.ChatID, Origin: req.Origin,
	})
	if err != nil || continued.ChatID != started.ChatID || continued.AgentKey != "mock-agent" || continued.RunID == started.RunID {
		t.Fatalf("continuation=%#v err=%v", continued, err)
	}
	waitRunTerminal(t, fixture.server, continued.RunID)
}
