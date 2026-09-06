package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type gatedQueryEngine struct{ entered, release chan struct{} }

func (e *gatedQueryEngine) Stream(ctx context.Context, _ api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	return &gatedQueryStream{ctx: ctx, engine: e}, nil
}

type gatedQueryStream struct {
	ctx    context.Context
	engine *gatedQueryEngine
	step   int
}

func (s *gatedQueryStream) Next() (contracts.AgentDelta, error) {
	s.step++
	switch s.step {
	case 1:
		return contracts.DeltaContent{Text: "before "}, nil
	case 2:
		close(s.engine.entered)
		select {
		case <-s.engine.release:
			return contracts.DeltaContent{Text: "after"}, nil
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	default:
		return nil, io.EOF
	}
}
func (*gatedQueryStream) Close() error { return nil }

func TestQueryDisconnectAndBlockingCallerPolicies(t *testing.T) {
	for _, entry := range []string{"sse", "json", "runtime"} {
		t.Run(entry, func(t *testing.T) {
			fixture := newTestFixture(t)
			engine := &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}
			fixture.server.deps.Agent = engine
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			response := httptest.NewRecorder()
			var result runtimetypes.QueryResult
			var runErr error
			go func() {
				defer close(finished)
				if entry == "runtime" {
					result, runErr = fixture.server.ExecuteQuery(ctx, runtimetypes.QueryCommand{AgentKey: "mock-agent", ChatID: "disconnect-chat", RunID: "disconnect-run", Message: "hello"}, runtimetypes.QueryHooks{})
					return
				}
				body := `{"agentKey":"mock-agent","chatId":"disconnect-chat","runId":"disconnect-run","message":"hello"`
				if entry == "json" {
					body += `,"stream":false`
				}
				body += "}"
				fixture.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(body)).WithContext(ctx))
			}()
			select {
			case <-engine.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("producer did not start")
			}
			cancel()
			if entry == "sse" {
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("disconnected SSE did not release observer")
				}
				subscription, err := fixture.server.deps.Runtime.AttachRun(context.Background(), runtimetypes.RunRef{RunID: "disconnect-run", ChatID: "disconnect-chat", AgentKey: "mock-agent"}, 0)
				if err != nil {
					t.Fatal(err)
				}
				attached := make(chan string, 1)
				go func() {
					defer subscription.Close()
					var content strings.Builder
					for event := range subscription.Events {
						if event.Type == "content.delta" {
							content.WriteString(event.String("delta"))
						}
					}
					attached <- content.String()
				}()
				close(engine.release)
				select {
				case content := <-attached:
					if content != "before after" {
						t.Fatalf("attached content=%q", content)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("attach did not finish")
				}
			} else {
				close(engine.release)
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("blocking caller did not finish")
				}
				if entry == "runtime" {
					if runErr != nil || result.Completion == nil || result.Completion.FinishReason != "complete" || result.Content != "before after" {
						t.Fatalf("result=%#v err=%v", result, runErr)
					}
				} else if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "before after") {
					t.Fatalf("response=%d %s", response.Code, response.Body.String())
				}
			}
		})
	}
}
