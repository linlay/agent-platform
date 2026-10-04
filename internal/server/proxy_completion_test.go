package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"

	gws "github.com/gorilla/websocket"
)

// Exercise the blocking entry used by automation, including the default Stream
// value, as well as the stream:false entry used by HTTP JSON callers.
func TestProxyBlockingCompletionMatchesPersistedRun(t *testing.T) {
	for _, transport := range []string{"ws", "sse"} {
		for _, nonStream := range []bool{false, true} {
			for _, reason := range []string{"complete", "error", "cancel"} {
				t.Run(fmt.Sprintf("%s/nonStream=%t/%s", transport, nonStream, reason), func(t *testing.T) {
					events := []map[string]any{
						{"type": "content.start", "contentId": "content-1"},
						{"type": "content.delta", "contentId": "content-1", "delta": "proxy result"},
						{"type": "content.end", "contentId": "content-1"},
						{"type": "run." + reason, "usage": map[string]any{"promptTokens": 3, "completionTokens": 2, "totalTokens": 5}},
					}
					for i, event := range events {
						event["seq"] = i + 1
						event["runId"] = "upstream-run"
						event["timestamp"] = time.Now().UnixMilli()
					}
					upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if transport == "ws" {
							upgrader := gws.Upgrader{}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							defer conn.Close()
							var request map[string]any
							if err := conn.ReadJSON(&request); err != nil {
								t.Error(err)
								return
							}
							for _, event := range events {
								if err := conn.WriteJSON(map[string]any{"event": event}); err != nil {
									t.Error(err)
									return
								}
							}
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						for _, event := range events {
							data, _ := json.Marshal(event)
							fmt.Fprintf(w, "data: %s\n\n", data)
						}
					}))
					t.Cleanup(upstream.Close)
					fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
						t.Error("proxy query unexpectedly called native model")
					}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
						writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
							"key: mock-agent", "name: Proxy", "role: test", "description: completion regression", "mode: PROXY",
							"proxyConfig:", "  baseUrl: " + upstream.URL, "  transport: " + transport,
						})
					}})
					cmd := runtimetypes.QueryCommand{AgentKey: "mock-agent", ChatID: "proxy-completion-chat", RunID: "proxy-completion-run", Message: "hello"}
					if nonStream {
						stream := false
						cmd.Stream = &stream
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result, err := fixture.server.deps.Runtime.ExecuteQueryWithHooks(ctx, cmd, runtimetypes.QueryHooks{})
					if reason == "complete" && err != nil {
						t.Fatalf("execute: %v", err)
					}
					if result.Completion == nil {
						t.Fatalf("completion missing: result=%+v err=%v", result, err)
					}
					completion := result.Completion
					if completion.FinishReason != reason || completion.AssistantText != "proxy result" || completion.Usage.TotalTokens != 5 {
						t.Fatalf("unexpected completion: %+v", completion)
					}
					if result.Content != completion.AssistantText || result.FinishReason != completion.FinishReason || !reflect.DeepEqual(result.Usage, completion.Usage) {
						t.Fatalf("result differs from completion: %+v", result)
					}
					runs, err := fixture.server.deps.Chats.ListRuns(cmd.ChatID)
					if err != nil || len(runs) != 1 {
						t.Fatalf("persisted runs: %+v, %v", runs, err)
					}
					if runs[0].RunID != completion.RunID || runs[0].FinishReason != reason || runs[0].StartedAt != completion.StartedAtMillis || runs[0].CompletedAt != completion.UpdatedAtMillis || runs[0].AssistantText != completion.AssistantText || !reflect.DeepEqual(runs[0].Usage, completion.Usage) {
						t.Fatalf("persisted run differs from completion: %+v vs %+v", runs[0], completion)
					}
				})
			}
		}
	}
}
