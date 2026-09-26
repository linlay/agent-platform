package server

import (
	"context"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const (
	awaitingPendingCode    = "awaiting_pending"
	awaitingPendingMessage = "pending awaiting found for chat"
)

type registeredQueryRun struct {
	RunCtx          context.Context
	Control         *contracts.RunControl
	Managed         bool
	StartedAtMillis int64
}

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

// registeredQueryRun reads the single authoritative lifecycle timestamp from
// the run manager immediately after registration.  Every subsequent
// persistence and push path receives this same value; never infer it from a
// run ID or a later completion timestamp.
