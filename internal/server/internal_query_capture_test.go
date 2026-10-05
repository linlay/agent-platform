package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

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

// queryResponseBuffer encodes legacy test responses only. Production callers
// obtain Native and Proxy results directly from Runtime.
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
