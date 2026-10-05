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

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestRunQueryOptionalSettings(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{configure: func(cfg *config.Config) { cfg.RunQuery.AllowAccessLevelOverride = enabled }})
			bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
			_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "full_access"})
			defer fixture.runs.Finish("parent")
			for _, level := range []string{"", "default", "auto_approve", "full_access"} {
				req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "run message", AccessLevel: level, MustUseSkills: []string{"mock-skill"}, ChatName: "自定义名称 " + level, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "tool"}}
				started, err := fixture.server.StartRun(context.Background(), req)
				if !enabled && level != "" && level != "default" {
					var typed *contracts.RunToolError
					if !errors.As(err, &typed) || typed.Code != "run_access_level_override_disabled" {
						t.Fatalf("level=%s err=%v", level, err)
					}
					continue
				}
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
		})
	}
}

func TestRunQueryTargetAdmissionForOptions(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) { cfg.RunQuery.AllowAccessLevelOverride = true },
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
		{contracts.RunStartRequest{TeamID: "default", MustUseSkills: []string{"demo"}}, "must_use_skills_unsupported"},
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

func TestRunQueryInheritsLiveParentAccessLevel(t *testing.T) {
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
