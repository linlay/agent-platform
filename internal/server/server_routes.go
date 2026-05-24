package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/observability"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	if s.handleCORS(w, r) {
		return
	}
	r = s.withPrincipal(r, w)
	if r == nil {
		return
	}
	if s.deps.Config.Logging.Request.Enabled {
		log.Printf("%s %s (arrived)", r.Method, observability.SanitizeLog(r.URL.RequestURI()))
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.router.ServeHTTP(rec, r)
	s.logRequest(r, rec.status, time.Since(startedAt))
}

func (s *Server) WSHandler() *ws.Handler {
	if s == nil {
		return nil
	}
	return s.wsHandler
}

func (s *Server) ExecuteInternalQuery(ctx context.Context, req api.QueryRequest) (int, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, "", err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)).WithContext(withSyncQueryContext(ctx))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleQuery(rec, httpReq)
	return rec.Code, strings.TrimSpace(rec.Body.String()), nil
}

// ExecuteInternalQueryStream reuses the normal query pipeline for in-process
// callers that need to react to each SSE event as it is emitted (e.g. the
// gateway bridge). onEvent receives the raw JSON payload of each `data:` line
// except the `[DONE]` sentinel. Returning an error from onEvent aborts further
// streaming but does not cancel the underlying run.

func (s *Server) ExecuteInternalQueryStream(
	ctx context.Context,
	req api.QueryRequest,
	onEvent func(eventJSON []byte) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)).WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	rw := newSSEInterceptor(onEvent)
	s.handleQuery(rw, httpReq)
	return rw.err
}

// ExecuteInternalSubmit reuses /api/submit for in-process callers (gateway
// bridge HITL submit). Returns the full JSON response body so callers can
// forward it verbatim.

func (s *Server) ExecuteInternalSubmit(ctx context.Context, req api.SubmitRequest) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(body)).WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleSubmit(rec, httpReq)
	return rec.Code, rec.Body.Bytes(), nil
}

// ExecuteInternalUpload reuses /api/upload for in-process callers. Accepts raw
// file bytes and metadata, returns the raw JSON response body.

func (s *Server) ExecuteInternalUpload(ctx context.Context, chatID, requestID, fileName, mimeType string, fileData []byte) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if requestID != "" {
		_ = writer.WriteField("requestId", requestID)
	}
	if chatID != "" {
		_ = writer.WriteField("chatId", chatID)
	}
	part, err := createMultipartFilePart(writer, "file", fileName, mimeType)
	if err != nil {
		return 0, nil, err
	}
	if _, err := part.Write(fileData); err != nil {
		return 0, nil, err
	}
	if err := writer.Close(); err != nil {
		return 0, nil, err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/upload", &buf).WithContext(ctx)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleUpload(rec, httpReq)
	return rec.Code, rec.Body.Bytes(), nil
}

func createMultipartFilePart(writer *multipart.Writer, fieldName, fileName, mimeType string) (io.Writer, error) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, fileName))
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	header.Set("Content-Type", mimeType)
	return writer.CreatePart(header)
}

// ResolveResourcePath returns the absolute local path for a `file` param in the
// /api/resource URL (e.g. "chatId/artifacts/runId/foo.docx"). Used by the
// gateway bridge to read artifact bytes off local disk without re-downloading.

func (s *Server) ResolveResourcePath(fileParam string) (string, error) {
	return s.deps.Chats.ResolveResource(fileParam)
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

func withSyncQueryContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, syncQueryContextKey{}, true)
}

func isSyncQueryContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(syncQueryContextKey{}).(bool)
	return value
}

func (s *Server) routes() {
	s.router.HandleFunc("/api/agents", s.method(http.MethodGet, s.handleAgents))
	s.router.HandleFunc("/api/channels", s.method(http.MethodGet, s.handleChannels))
	s.router.HandleFunc("/api/agent", s.method(http.MethodGet, s.handleAgent))
	s.router.HandleFunc("/api/agent/create", s.method(http.MethodPost, s.handleAgentCreate))
	s.router.HandleFunc("/api/agent/update", s.method(http.MethodPost, s.handleAgentUpdate))
	s.router.HandleFunc("/api/agent/delete", s.method(http.MethodPost, s.handleAgentDelete))
	s.router.HandleFunc("/api/agent/editor-options", s.method(http.MethodGet, s.handleAgentEditorOptions))
	s.router.HandleFunc("/api/teams", s.method(http.MethodGet, s.handleTeams))
	s.router.HandleFunc("/api/skills", s.method(http.MethodGet, s.handleSkills))
	s.router.HandleFunc("/api/skill-candidates", s.method(http.MethodGet, s.handleSkillCandidates))
	s.router.HandleFunc("/api/tools", s.method(http.MethodGet, s.handleTools))
	s.router.HandleFunc("/api/tool", s.method(http.MethodGet, s.handleTool))
	s.router.HandleFunc("/api/chats", s.method(http.MethodGet, s.handleChats))
	s.router.HandleFunc("/api/chat", s.method(http.MethodGet, s.handleChat))
	s.router.HandleFunc("/api/chats/search", s.method(http.MethodPost, s.handleGlobalSearch))
	s.router.HandleFunc("/api/read", s.method(http.MethodPost, s.handleRead))
	s.router.HandleFunc("/api/feedback", s.method(http.MethodPost, s.handleFeedback))
	s.router.HandleFunc("/api/chat/delete", s.method(http.MethodPost, s.handleChatDelete))
	s.router.HandleFunc("/api/chat/rename", s.method(http.MethodPost, s.handleChatRename))
	s.router.HandleFunc("/api/chat/archive", s.method(http.MethodPost, s.handleChatArchive))
	s.router.HandleFunc("/api/archives", s.method(http.MethodGet, s.handleArchives))
	s.router.HandleFunc("/api/archive", s.method(http.MethodGet, s.handleArchive))
	s.router.HandleFunc("/api/archives/search", s.method(http.MethodPost, s.handleArchiveSearch))
	s.router.HandleFunc("/api/archive/delete", s.method(http.MethodPost, s.handleArchiveDelete))
	s.router.HandleFunc("/api/chat/export", s.method(http.MethodGet, s.handleChatExport))
	s.router.HandleFunc("/api/automations", s.method(http.MethodPost, s.handleAutomations))
	s.router.HandleFunc("/api/automation", s.method(http.MethodPost, s.handleAutomation))
	s.router.HandleFunc("/api/automation/create", s.method(http.MethodPost, s.handleAutomationCreate))
	s.router.HandleFunc("/api/automation/update", s.method(http.MethodPost, s.handleAutomationUpdate))
	s.router.HandleFunc("/api/automation/delete", s.method(http.MethodPost, s.handleAutomationDelete))
	s.router.HandleFunc("/api/automation/toggle", s.method(http.MethodPost, s.handleAutomationToggle))
	s.router.HandleFunc("/api/automation/executions", s.method(http.MethodPost, s.handleAutomationExecutions))
	s.router.HandleFunc("/api/query", s.method(http.MethodPost, s.handleQuery))
	s.router.HandleFunc("/api/attach", s.method(http.MethodGet, s.handleAttach))
	s.router.HandleFunc("/api/submit", s.method(http.MethodPost, s.handleSubmit))
	s.router.HandleFunc("/api/steer", s.method(http.MethodPost, s.handleSteer))
	s.router.HandleFunc("/api/interrupt", s.method(http.MethodPost, s.handleInterrupt))
	s.router.HandleFunc("/api/remember", s.method(http.MethodPost, s.handleRemember))
	s.router.HandleFunc("/api/learn", s.method(http.MethodPost, s.handleLearn))
	s.router.HandleFunc("/api/memory/meta", s.method(http.MethodGet, s.handleMemoryMeta))
	s.router.HandleFunc("/api/memory/context-preview", s.method(http.MethodPost, s.handleMemoryContextPreview))
	s.router.HandleFunc("/api/memory/scope/list", s.method(http.MethodGet, s.handleMemoryScopes))
	s.router.HandleFunc("/api/memory/scope/detail", s.method(http.MethodGet, s.handleMemoryScope))
	s.router.HandleFunc("/api/memory/scope/save", s.method(http.MethodPost, s.handleMemoryScopeSave))
	s.router.HandleFunc("/api/memory/scope/validate", s.method(http.MethodPost, s.handleMemoryScopeValidate))
	s.router.HandleFunc("/api/memory/record/list", s.method(http.MethodGet, s.handleMemoryRecords))
	s.router.HandleFunc("/api/memory/history", s.method(http.MethodGet, s.handleMemoryHistory))
	s.router.HandleFunc("/api/memory/record/detail", s.method(http.MethodGet, s.handleMemoryRecord))
	s.router.HandleFunc("/api/memory/record/timeline", s.method(http.MethodGet, s.handleMemoryRecordTimeline))
	s.router.HandleFunc("/api/viewport", s.method(http.MethodGet, s.handleViewport))
	s.router.HandleFunc("/api/resource", s.method(http.MethodGet, s.handleResource))
	s.router.HandleFunc("/api/upload", s.method(http.MethodPost, s.handleUpload))
	if s.wsHandler != nil {
		s.router.Handle("/ws", s.wsHandler)
	}
}

func (s *Server) method(expected string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != expected {
			w.Header().Set("Allow", expected)
			writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
			return
		}
		handler(w, r)
	}
}

// enrichToolMetadata fills display fields on tool.snapshot events by looking up
// the tool definition in the registry. LoadChat reconstructs these events from
// JSONL which only has the raw tool name.
