package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream" // These legacy in-process entry points only encode Runtime results/events.
	"bytes"
	"context"
	"net/http"
)

func (s *Server) ExecuteInternalQuery(ctx context.Context, req api.QueryRequest) (int, string, error) {
	result, err := s.ExecuteInternalQueryResult(ctx, req, InternalQueryHooks{})
	return result.StatusCode, result.Body, err
}
func (s *Server) ExecuteInternalQueryResult(ctx context.Context, req api.QueryRequest, hooks InternalQueryHooks) (InternalQueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := trustedQueryCommand(ctx, req)
	response := newQueryResponseBuffer()
	capture := &internalQueryCapture{hooks: hooks}
	ctx = withInternalQueryCapture(ctx, capture)
	writer, err := newSSEWriter(response, sseWriterOptions{SSE: s.deps.Config.SSE, Render: stream.DefaultRenderConfig()})
	if err != nil {
		return InternalQueryResult{}, err
	}
	defer writer.Close()
	result, err := s.deps.Runtime.ExecuteQuery(ctx, cmd, &internalQueryEventSink{ctx: ctx, writer: writer})
	if err != nil {
		writeQueryStartError(response, err)
	} else {
		_ = writer.WriteDone()
	}
	capture.completion = result.Completion
	capture.errorMessage = result.ErrorMessage
	return capture.result(response.status, response.body.String()), nil
}

type internalQueryEventSink struct {
	ctx    context.Context
	writer *sseWriter
}

func (s *internalQueryEventSink) OnRunStarted(start chat.RunStart) {
	notifyInternalQueryRunStarted(s.ctx, start)
}
func (s *internalQueryEventSink) Emit(ctx context.Context, event stream.EventData) error {
	return s.writer.WriteJSON("message", event)
}
func (s *Server) ExecuteInternalQueryStream(ctx context.Context, req api.QueryRequest, onEvent func([]byte) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rw := newSSEInterceptor(onEvent)
	s.writeRuntimeQueryResponse(rw, ctx, trustedQueryCommand(ctx, req), stream.DetailFull)
	return rw.err
}

type sseInterceptor struct {
	header  http.Header
	buf     bytes.Buffer
	onEvent func([]byte) error
	err     error
}

func newSSEInterceptor(onEvent func([]byte) error) *sseInterceptor {
	return &sseInterceptor{header: http.Header{}, onEvent: onEvent}
}

func (w *sseInterceptor) Header() http.Header { return w.header }

func (w *sseInterceptor) WriteHeader(int) {}

func (w *sseInterceptor) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		idx := bytes.Index(w.buf.Bytes(), []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := make([]byte, idx)
		copy(frame, w.buf.Bytes()[:idx])
		w.buf.Next(idx + 2)
		var payload []byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				chunk := bytes.TrimPrefix(line, []byte("data:"))
				chunk = bytes.TrimPrefix(chunk, []byte(" "))
				if len(payload) > 0 {
					payload = append(payload, '\n')
				}
				payload = append(payload, chunk...)
			}
		}
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte(stream.DoneSentinel)) {
			continue
		}
		if w.err == nil && w.onEvent != nil {
			if err := w.onEvent(payload); err != nil {
				w.err = err
			}
		}
	}
	return n, nil
}

func (w *sseInterceptor) Flush() {}
