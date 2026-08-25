package server

import (
	"context"
	"log"
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
