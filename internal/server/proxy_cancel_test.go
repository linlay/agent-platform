package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"

	gws "github.com/gorilla/websocket"
)

func TestSilentProxyWebSocketCancellationClosesConnection(t *testing.T) {
	for _, trigger := range []string{"interrupt", "shutdown"} {
		t.Run(trigger, func(t *testing.T) {
			connected := make(chan *gws.Conn, 1)
			closed := make(chan struct{})
			upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				connected <- conn
				// Keep reading controls, but never send an event or a terminal reply.
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						close(closed)
						return
					}
				}
			}))
			t.Cleanup(upstream.Close)
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("proxy unexpectedly called native model")
			}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
				writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
					"key: mock-agent", "name: Proxy", "role: test", "description: cancellation regression", "mode: PROXY",
					"proxyConfig:", "  baseUrl: " + upstream.URL, "  transport: ws",
				})
			}})
			rootCtx, shutdown := context.WithCancel(context.Background())
			defer shutdown()
			fixture.server.backgroundCtx = rootCtx
			bindTestRuntime(fixture.server)
			handle, err := fixture.server.deps.Runtime.StartQuery(context.Background(), runtimetypes.QueryCommand{
				AgentKey: "mock-agent", ChatID: "silent-proxy-chat", RunID: "silent-proxy-run", Message: "hello",
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case conn := <-connected:
				defer conn.Close()
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not receive query")
			}
			subscription, err := fixture.server.deps.Runtime.AttachRun(context.Background(), runtimetypes.RunRef{RunID: handle.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer subscription.Close()
			if trigger == "shutdown" {
				shutdown()
			} else {
				result, err := fixture.server.deps.Runtime.Interrupt(context.Background(), runtimetypes.InterruptCommand{
					RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: handle.ChatID, AgentKey: handle.AgentKey},
				})
				if err != nil || !result.Accepted {
					t.Fatalf("interrupt: %+v, %v", result, err)
				}
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("cancellation left silent upstream websocket open")
			}
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case _, ok := <-subscription.Events:
					if !ok {
						runs, err := fixture.chats.ListRuns(handle.ChatID)
						if err != nil || len(runs) != 1 || runs[0].CompletedAt == 0 {
							t.Fatalf("run did not persist completion: %+v, %v", runs, err)
						}
						return
					}
				case <-timer.C:
					t.Fatal("cancellation did not finish proxy event stream")
				}
			}
		})
	}
}
