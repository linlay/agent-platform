package runops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/toolinput"
)

type idempotentSteer struct {
	done   chan struct{}
	digest string
	result contracts.ToolExecutionResult
}

func steerID(origin contracts.RunOrigin) string {
	digest := sha256.Sum256([]byte(startKey(origin)))
	return "steer_" + hex.EncodeToString(digest[:16])
}

func cloneSteerResult(result contracts.ToolExecutionResult) contracts.ToolExecutionResult {
	result.Structured = contracts.CloneMap(result.Structured)
	return result
}

func (h *ToolHandler) steer(ctx context.Context, args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
	if origin.RunID == "" || origin.ToolID == "" || origin.ChatID == "" {
		return errorResult("run_context_required", "chat_steer requires caller runId, chatId and toolId"), nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return errorResult("invalid_request", "arguments must be JSON values"), nil
	}
	digest := sha256.Sum256(encoded)
	requestDigest := hex.EncodeToString(digest[:])
	key := startKey(origin) + "\x00" + origin.AgentKey + "\x00" + origin.Subject
	h.mu.Lock()
	h.cleanupIdempotencyLocked()
	receipt, exists := h.steers[key]
	if !exists {
		receipt = &idempotentSteer{done: make(chan struct{}), digest: requestDigest}
		h.steers[key] = receipt
	}
	h.mu.Unlock()
	if receipt.digest != requestDigest {
		return resultFromError(&contracts.RunToolError{Code: "idempotency_conflict", ExecutionState: "not_started", Message: "this chat_steer invocation already used different arguments"}), nil
	}
	if exists {
		// Unlike chat_start, even a definite rejection is frozen. A replay is
		// never a new enqueue attempt; only a new tool invocation may send again.
		<-receipt.done
		return cloneSteerResult(receipt.result), nil
	}
	result := h.invokeSteer(ctx, args, origin)
	result.Structured["steerId"] = steerID(origin)
	if result.Error != "" && result.Structured["executionState"] == nil {
		result.Structured["executionState"] = "not_started"
	}
	data, _ := json.Marshal(result.Structured)
	result.Output = string(data)
	h.mu.Lock()
	receipt.result = cloneSteerResult(result)
	close(receipt.done)
	h.mu.Unlock()
	// Retain the first outcome for this process, even after the caller ends.
	// Replaying an old invocation must not turn a rejection into a new send.
	return cloneSteerResult(result), nil
}

func (h *ToolHandler) invokeSteer(ctx context.Context, args map[string]any, origin contracts.RunOrigin) contracts.ToolExecutionResult {
	if err := toolinput.Validate(args, map[string]string{"runId": "s!", "message": "s!"}, ""); err != nil {
		return resultFromError(err)
	}
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	if runID == "" || message == "" {
		return errorResult("invalid_request", "runId and message must be non-empty strings")
	}
	if err := ctx.Err(); err != nil {
		return errorResult("chat_steer_cancelled", err.Error())
	}
	if runID == origin.RunID {
		return errorResult("run_not_owned", "chat_steer cannot address the calling run")
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result
	}
	snapshot, err := h.service.GetRunStatus(runID)
	if err != nil {
		return resultFromError(err)
	}
	response, err := h.service.Steer(ctx, runtimetypes.SteerCommand{
		RunRef:  runtimetypes.RunRef{RunID: runID, ChatID: snapshot.ChatID, AgentKey: snapshot.AgentKey},
		SteerID: steerID(origin), Message: message,
		AgentCaller: &runtimetypes.SteerCaller{
			AgentKey: origin.AgentKey, Subject: origin.Subject,
			ChatID: origin.ChatID, RunID: origin.RunID, ToolID: origin.ToolID,
		},
	})
	if err != nil {
		return resultFromError(err)
	}
	if updated, err := h.service.GetRunStatus(runID); err == nil {
		snapshot = updated
	}
	result := successResult("steer", response.Accepted, response.Status, snapshot)
	result.Structured["detail"] = response.Detail
	return result
}
