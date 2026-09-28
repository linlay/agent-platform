package query

import (
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts/queryinput"
)

func awaitingSubmitStatusError(req queryinput.SubmitRequest, chatID string, status string, errorCode string, detail string, httpStatus int) error {
	chatID = strings.TrimSpace(chatID)
	response := queryinput.SubmitResponse{
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
		Status:  httpStatus,
		Code:    response.ErrorCode,
		Message: response.Detail,
		Data:    data,
	}
}

func awaitingSubmitConflictError(req queryinput.SubmitRequest, chatID string, status string, errorCode string, detail string) error {
	return awaitingSubmitStatusError(req, chatID, status, errorCode, detail, 409)
}

func unknownAwaitingSubmitError(req queryinput.SubmitRequest) error {
	return awaitingSubmitStatusError(req, activeSubmitChatID(nil, req), "unknown", string(apperrors.CodeUnknownAwaiting), "unknown awaitingId", 400)
}
