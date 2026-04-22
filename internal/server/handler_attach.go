package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/stream"
)

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	lastSeqStr := strings.TrimSpace(r.URL.Query().Get("lastSeq"))
	var lastSeq int64
	if lastSeqStr != "" {
		parsed, err := strconv.ParseInt(lastSeqStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "lastSeq must be a valid integer"))
			return
		}
		lastSeq = parsed
	}

	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "run not found"))
		return
	}

	observer, err := s.deps.Runs.AttachObserver(runID, lastSeq)
	if err != nil {
		var replayErr *stream.ReplayWindowExceededError
		if errors.As(err, &replayErr) {
			writeJSON(w, http.StatusConflict, api.ApiResponse[map[string]any]{
				Code: http.StatusConflict,
				Msg:  "SEQ_EXPIRED",
				Data: map[string]any{
					"code":      "SEQ_EXPIRED",
					"runId":     runID,
					"chatId":    status.ChatID,
					"oldestSeq": replayErr.OldestSeq,
					"latestSeq": replayErr.LatestSeq,
					"lastSeq":   replayErr.AfterSeq,
				},
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(runID, observer.ID)
	defer observer.MarkDone()

	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", event); err != nil {
				return
			}
		}
	}
}
