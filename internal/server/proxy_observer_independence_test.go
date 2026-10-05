package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestProxySSEExecutionIndependentOfObservers(t *testing.T) {
	for _, entry := range []string{"no_observer", "observer_disconnect", "blocking", "blocking_json"} {
		t.Run(entry, func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			upstreamReady := make(chan struct{})
			upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var query map[string]any
				if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
					t.Error(err)
					return
				}
				if query["stream"] != true {
					t.Error("all Proxy callers must request upstream streaming")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				seq := 7 // A real initial gap must not prevent a later observer attaching.
				emit := func(kind string, payload map[string]any) {
					payload["type"], payload["seq"], payload["timestamp"] = kind, seq, time.Now().UnixMilli()
					payload["runId"], payload["chatId"] = "upstream-run", "upstream-chat"
					seq++
					data, _ := json.Marshal(payload)
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.(http.Flusher).Flush()
				}
				emit("content.start", map[string]any{"contentId": "answer"})
				emit("content.delta", map[string]any{"contentId": "answer", "delta": "before "})
				close(upstreamReady)
				select {
				case <-release:
				case <-r.Context().Done():
					t.Error("observer/caller cancellation stopped upstream execution")
					return
				}
				emit("content.delta", map[string]any{"contentId": "answer", "delta": "after"})
				emit("content.end", map[string]any{"contentId": "answer"})
				emit("run.complete", map[string]any{"usage": map[string]any{"totalTokens": 5}})
			}))
			t.Cleanup(upstream.Close)
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(http.ResponseWriter, *http.Request) {
				t.Error("Proxy called Native model")
			}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
				writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
					"key: mock-agent", "name: Proxy", "role: test", "description: observer independence", "mode: PROXY",
					"proxyConfig:", "  baseUrl: " + upstream.URL, "  transport: sse",
				})
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := runtimetypes.QueryCommand{AgentKey: "mock-agent", ChatID: "observer-chat", RunID: "observer-run", Message: "hello", IncludeFullText: true}
			blocking := strings.HasPrefix(entry, "blocking")
			if entry == "blocking_json" {
				stream := false
				cmd.Stream = &stream
			}
			type outcome struct {
				result runtimetypes.QueryResult
				err    error
			}
			completed := make(chan outcome, 1)
			starts := make(chan chat.RunStart, 2)
			if blocking {
				go func() {
					result, err := fixture.server.deps.Runtime.ExecuteQueryWithHooks(ctx, cmd, runtimetypes.QueryHooks{OnRunStarted: func(start chat.RunStart) { starts <- start }})
					completed <- outcome{result, err}
				}()
			} else if _, err := fixture.server.deps.Runtime.StartQuery(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			select {
			case <-upstreamReady:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not start")
			}
			if entry == "observer_disconnect" {
				sub, err := fixture.server.deps.Runtime.AttachRun(ctx, runtimetypes.RunRef{RunID: cmd.RunID}, 0)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-sub.Events:
				case <-time.After(time.Second):
					t.Fatal("observer received no event")
				}
				sub.Close()
			}
			// Neither a departed observer nor a cancelled caller owns the Run.
			cancel()
			unblock()
			if blocking {
				select {
				case out := <-completed:
					if out.err != nil || out.result.Completion == nil || out.result.Content != "before after" || out.result.FinishReason != "complete" || out.result.Usage.TotalTokens != 5 || !strings.Contains(out.result.FullText, "before after") {
						t.Fatalf("blocking result=%+v err=%v", out.result, out.err)
					}
					if len(starts) != 1 {
						t.Fatalf("OnRunStarted count=%d", len(starts))
					}
				case <-time.After(2 * time.Second):
					t.Fatal("blocking call waited for a frontend observer")
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				runs, err := fixture.chats.ListRuns(cmd.ChatID)
				if err == nil && len(runs) == 1 && runs[0].CompletedAt != 0 {
					if runs[0].AssistantText != "before after" || runs[0].FinishReason != "complete" {
						t.Fatalf("persisted result=%+v", runs[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no completion without observers: %+v, %v", runs, err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			late, err := fixture.server.deps.Runtime.AttachRun(context.Background(), runtimetypes.RunRef{RunID: cmd.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer late.Close()
			var text strings.Builder
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			for {
				select {
				case event, ok := <-late.Events:
					if !ok {
						if text.String() != "before after" {
							t.Fatalf("late replay=%q", text.String())
						}
						return
					}
					if event.Type == "content.delta" {
						text.WriteString(event.String("delta"))
					}
				case <-timer.C:
					t.Fatal("late observer never reached completion")
				}
			}
		})
	}
}
