package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

type InternalQueryHooks struct {
	OnRunStarted func(chat.RunStart)
}

type InternalQueryResult struct {
	StatusCode   int
	Body         string
	Completion   *chat.RunCompletion
	ErrorMessage string
}

type internalQueryCaptureKey struct{}

type internalQueryCapture struct {
	mu           sync.Mutex
	hooks        InternalQueryHooks
	completion   *chat.RunCompletion
	errorMessage string
}

func withInternalQueryCapture(ctx context.Context, capture *internalQueryCapture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, internalQueryCaptureKey{}, capture)
}

func internalQueryCaptureFromContext(ctx context.Context) *internalQueryCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(internalQueryCaptureKey{}).(*internalQueryCapture)
	return capture
}

func notifyInternalQueryRunStarted(ctx context.Context, start chat.RunStart) {
	capture := internalQueryCaptureFromContext(ctx)
	if capture == nil || capture.hooks.OnRunStarted == nil {
		return
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("[server] internal query OnRunStarted panic recovered runID=%s err=%v", start.RunID, recovered)
			}
		}()
		capture.hooks.OnRunStarted(start)
	}()
}

func notifyInternalQueryCompletion(ctx context.Context, completion *chat.RunCompletion, errorMessage string) {
	capture := internalQueryCaptureFromContext(ctx)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if completion != nil {
		copy := *completion
		capture.completion = &copy
	}
	if message := strings.TrimSpace(errorMessage); message != "" {
		capture.errorMessage = message
	}
}

func (c *internalQueryCapture) result(statusCode int, body string) InternalQueryResult {
	if c == nil {
		return InternalQueryResult{StatusCode: statusCode, Body: strings.TrimSpace(body)}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := InternalQueryResult{
		StatusCode:   statusCode,
		Body:         strings.TrimSpace(body),
		ErrorMessage: c.errorMessage,
	}
	if c.completion != nil {
		copy := *c.completion
		result.Completion = &copy
	}
	return result
}

// prepareBlockingQuery shares admission/session construction with StartQuery;
// no HTTP request is manufactured for Native in-process execution.
func (s *Server) prepareBlockingQuery(ctx context.Context, req api.QueryRequest, locale, baseURL string) (preparedQuery, error) {
	admission, err := s.prepareQueryAdmissionRequest(ctx, req, true, locale, baseURL)
	if err != nil {
		return preparedQuery{}, err
	}
	return s.completeQueryPreparation(ctx, admission, nil)
}

// queryResponseBuffer is only a legacy response encoder. Runtime Native callers
// obtain the executor result directly and do not parse or capture HTTP/SSE.
type queryResponseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newQueryResponseBuffer() *queryResponseBuffer {
	return &queryResponseBuffer{header: http.Header{}, status: http.StatusOK}
}
func (w *queryResponseBuffer) Header() http.Header            { return w.header }
func (w *queryResponseBuffer) WriteHeader(status int)         { w.status = status }
func (w *queryResponseBuffer) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *queryResponseBuffer) Flush()                         {}

// Proxy keeps its existing upstream protocol driver and synchronous-context
// contract. This request carries transport metadata only; admission is done.
func (s *Server) executePreparedProxyCompatibility(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	req, _ := http.NewRequestWithContext(withSyncQueryContext(ctx), http.MethodPost, "/api/query", nil)
	if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "ws" {
		s.handleProxyWebSocketQuery(w, req, prepared)
	} else {
		s.handleProxyQuery(w, req, prepared)
	}
}
