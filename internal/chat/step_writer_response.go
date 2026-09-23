package chat

import (
	"agent-platform/internal/modelcontent"
	"strings"
)

// SetModelResponse stages private protocol state before opening the model
// commit gate. Snapshots may arrive after commit, so attach only during flush.
func (w *StepWriter) SetModelResponse(taskID, responseID string, parts []modelcontent.ReasoningPart) {
	if w == nil || w.persistenceErr != nil || (responseID == "" && len(parts) == 0) {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID != "" {
		b := w.taskBuffers[taskID]
		if b == nil || !b.modelTurnCommitRequired {
			return
		}
		b.responseID = responseID
		b.encryptedReasoning = append([]ContentPart(nil), parts...)
	} else {
		if !w.modelTurnCommitRequired {
			return
		}
		w.responseID = responseID
		w.encryptedReasoning = append([]ContentPart(nil), parts...)
	}
}
func attachResponseReasoning(messages []StoredMessage, parts []ContentPart, responseID string, timestamp int64) []StoredMessage {
	out := append([]StoredMessage(nil), messages...)
	if responseID == "" && len(parts) == 0 {
		return out
	}
	for i := range out {
		if out[i].Role == "assistant" {
			out[i].ReasoningContent = append(append([]ContentPart(nil), out[i].ReasoningContent...), parts...)
			return out
		}
	}
	// A reasoning-only terminal response still has a model-call identity.
	return append(out, StoredMessage{Role: "assistant", Ts: &timestamp, ReasoningContent: append([]ContentPart(nil), parts...)})
}
