package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
)

const (
	awaitingPendingCode    = "awaiting_pending"
	awaitingPendingMessage = "pending awaiting found for chat"
)

func writeStatusError(w http.ResponseWriter, err *statusError) {
	if err == nil {
		return
	}
	if err.Data != nil {
		msg := strings.TrimSpace(err.Code)
		if msg == "" {
			msg = err.Message
		}
		writeJSON(w, err.Status, api.ApiResponse[any]{
			Code: err.Status,
			Msg:  msg,
			Data: err.Data,
		})
		return
	}
	writeJSON(w, err.Status, api.Failure(err.Status, err.Message))
}
