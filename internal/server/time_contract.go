package server

import (
	"errors"
	"net/http"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/ws"
)

const timeContractViolationMessage = "time contract violation"
const chatStorageSchemaViolationMessage = "chat storage schema violation"

func writeTimeContractViolation(w http.ResponseWriter, err error) {
	if chat.IsJSONLSchemaViolation(err) {
		writeJSONUnchecked(w, http.StatusUnprocessableEntity, api.Failure(http.StatusUnprocessableEntity, chatStorageSchemaViolationMessage, chatStorageSchemaErrorData(err)))
		return
	}
	writeTimeContractViolationData(w, timeContractErrorData(err))
}

func writeTimeContractViolationData(w http.ResponseWriter, data map[string]any) {
	writeJSONUnchecked(w, http.StatusUnprocessableEntity, api.Failure(http.StatusUnprocessableEntity, timeContractViolationMessage, data))
}

func sendTimeContractViolation(conn *ws.Conn, requestID string, err error) {
	if conn == nil {
		return
	}
	if chat.IsJSONLSchemaViolation(err) {
		conn.SendError(requestID, string(apperrors.CodeChatStorageSchemaViolation), http.StatusUnprocessableEntity, chatStorageSchemaViolationMessage, chatStorageSchemaErrorData(err))
		return
	}
	// Expose field/location/expected directly in WS error data, just like the
	// HTTP 422 envelope. SendError still attaches the standard nested `error`
	// descriptor for clients which consume the common error shape.
	conn.SendError(requestID, string(apperrors.CodeTimeContractViolation), http.StatusUnprocessableEntity, timeContractViolationMessage, timeContractErrorData(err))
}

func timeContractErrorData(err error) map[string]any {
	if chat.IsJSONLSchemaViolation(err) {
		return chatStorageSchemaErrorData(err)
	}
	data := timecontract.ErrorData(err)
	data["category"] = string(apperrors.CategoryRequest)
	data["scope"] = string(apperrors.ScopeRequest)
	data["status"] = http.StatusUnprocessableEntity
	data["retryable"] = false
	data["userSafeMessageKey"] = string(apperrors.CodeTimeContractViolation)
	data["message"] = timeContractViolationMessage
	return data
}

func chatStorageSchemaErrorData(err error) map[string]any {
	data := chat.JSONLSchemaErrorData(err)
	data["category"] = string(apperrors.CategoryChatRun)
	data["scope"] = string(apperrors.ScopeChat)
	data["status"] = http.StatusUnprocessableEntity
	data["retryable"] = false
	data["userSafeMessageKey"] = string(apperrors.CodeChatStorageSchemaViolation)
	data["message"] = chatStorageSchemaViolationMessage
	return data
}

func contractViolationMessage(err error) string {
	if chat.IsJSONLSchemaViolation(err) {
		return chatStorageSchemaViolationMessage
	}
	return timeContractViolationMessage
}

// localTimeContractRunErrorEvent replaces an invalid upstream event after a
// stream has started. Its timestamp belongs to the platform error itself, not
// to the rejected event, so using the local wall clock here does not repair or
// reinterpret producer data. Keep contract details both flat and under
// `error` for existing stream consumers.
func localTimeContractRunErrorEvent(seq int64, runID, chatID string, err error) stream.EventData {
	if seq <= 0 {
		seq = 1
	}
	contractData := timeContractErrorData(err)
	contractData["status"] = http.StatusUnprocessableEntity
	message := contractViolationMessage(err)
	contractData["message"] = message
	payload := map[string]any{
		"runId":   runID,
		"chatId":  chatID,
		"message": message,
		"error":   contractData,
	}
	for _, key := range []string{"code", "field", "location", "expected"} {
		payload[key] = contractData[key]
	}
	return stream.EventData{
		Seq:       seq,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

// nextLocalTimeContractErrorSeq allocates a terminal event sequence without
// reusing a sequence already delivered to this observer. The rejected source
// event is deliberately not repaired or forwarded; when it already had a
// usable sequence, the platform-owned error occupies that sequence instead.
func nextLocalTimeContractErrorSeq(lastSeq int64, rejected stream.EventData) int64 {
	seq := rejected.Seq
	if seq <= lastSeq {
		seq = lastSeq + 1
	}
	if seq <= 0 {
		seq = 1
	}
	return seq
}

// terminateSSEForTimeContractViolation is the final SSE observer boundary.
// The invalid event has already been rejected by Writer.WriteJSON, so write
// exactly one platform-owned run.error and the SSE done sentinel. Then cancel
// and finish an active run so an upstream producer cannot continue after its
// client-visible stream has been terminated. A completed replay is never
// finished again: that would rewrite its completion lifecycle timestamp.
func (s *Server) terminateSSEForTimeContractViolation(
	sseWriter *stream.Writer,
	lastSeq int64,
	rejected stream.EventData,
	run api.InterruptRequest,
	err error,
) {
	if sseWriter != nil {
		local := localTimeContractRunErrorEvent(
			nextLocalTimeContractErrorSeq(lastSeq, rejected),
			run.RunID,
			run.ChatID,
			err,
		)
		_ = sseWriter.WriteJSON("message", local)
		_ = sseWriter.WriteDone()
	}
	if s == nil || s.deps.Runs == nil || run.RunID == "" {
		return
	}
	status, ok := s.deps.Runs.RunStatus(run.RunID)
	if !ok || status.CompletedAt != 0 {
		// A replay can surface a malformed historic event after its run already
		// completed. It still receives the local terminal error above, but must
		// not be interrupted: RunControl.Interrupt would otherwise turn a
		// completed lifecycle into cancelled.
		return
	}
	run = interruptRequestWithCause(
		run,
		contracts.InterruptSourceHTTPAPI,
		contracts.InterruptReasonRunInterrupted,
		contractViolationMessage(err),
	)
	s.deps.Runs.Interrupt(run)
	if status, ok := s.deps.Runs.RunStatus(run.RunID); ok && status.CompletedAt == 0 {
		s.deps.Runs.Finish(run.RunID)
	}
}

func isTimeContractViolation(err error) bool {
	return errors.Is(err, errTimeContractViolation) || timecontract.IsViolation(err) || chat.IsJSONLSchemaViolation(err)
}

func timeContractStatusError(err error) *statusError {
	if !isTimeContractViolation(err) {
		return nil
	}
	code := "time_contract_violation"
	message := timeContractViolationMessage
	if chat.IsJSONLSchemaViolation(err) {
		code = chat.ChatStorageSchemaViolationCode
		message = chatStorageSchemaViolationMessage
	}
	return &statusError{
		status:  http.StatusUnprocessableEntity,
		code:    code,
		message: message,
		data:    timeContractErrorData(err),
	}
}

// errTimeContractViolation lets handler paths which already parsed a detailed
// invalid JSON record return the same public error without manufacturing a
// current timestamp or a generic storage error.
var errTimeContractViolation = errors.New("time contract violation")

// validatePublicTimeContract remains as a narrow compatibility hook for
// handlers that already own a typed DTO. There is intentionally no generic
// JSON traversal here: external tool and bridge payloads can legally contain
// business properties named createdAt, timestamp, or iso. Platform DTOs must
// validate their declared time fields at their producer/read boundary.
func validatePublicTimeContract(_ any) error { return nil }
