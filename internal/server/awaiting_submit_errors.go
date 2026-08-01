package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
)

func unknownAwaitingSubmitError(req api.SubmitRequest) error {
	return awaitingSubmitStatusError(req, activeSubmitChatID(nil, req), "unknown", string(apperrors.CodeUnknownAwaiting), "unknown awaitingId", http.StatusBadRequest)
}

func awaitingSubmitConflictError(req api.SubmitRequest, chatID string, status string, errorCode string, detail string) error {
	return awaitingSubmitStatusError(req, chatID, status, errorCode, detail, http.StatusConflict)
}

func awaitingSubmitStatusError(req api.SubmitRequest, chatID string, status string, errorCode string, detail string, httpStatus int) error {
	chatID = strings.TrimSpace(chatID)
	response := api.SubmitResponse{
		Accepted:   false,
		Status:     strings.TrimSpace(status),
		ChatID:     chatID,
		RunID:      strings.TrimSpace(req.RunID),
		AwaitingID: strings.TrimSpace(req.AwaitingID),
		SubmitID:   strings.TrimSpace(req.SubmitID),
		ErrorCode:  strings.TrimSpace(errorCode),
		Detail:     strings.TrimSpace(detail),
	}
	appCode := apperrors.Code(errorCode)
	data := map[string]any{
		"accepted":   response.Accepted,
		"status":     response.Status,
		"chatId":     response.ChatID,
		"runId":      response.RunID,
		"awaitingId": response.AwaitingID,
		"errorCode":  response.ErrorCode,
		"detail":     response.Detail,
		"error": apperrors.Payload(
			appCode,
			response.Detail,
			apperrors.WithStatus(httpStatus),
			apperrors.WithRetryable(false),
			apperrors.WithDiagnostic("chatId", response.ChatID),
			apperrors.WithDiagnostic("runId", response.RunID),
			apperrors.WithDiagnostic("awaitingId", response.AwaitingID),
		),
	}
	if response.SubmitID != "" {
		data["submitId"] = response.SubmitID
	}
	return &statusError{
		status:  httpStatus,
		code:    response.ErrorCode,
		message: response.Detail,
		data:    data,
	}
}
