package llm

import (
	"agent-platform/internal/modelresponses"
	"encoding/json"
	"strings"
	"time"
)

// Only a structured rejection of encrypted state permits this recovery. HTTP
// timeouts and generic 400s must not silently change conversation semantics.
func (s *llmRunStream) recoverResponsesState(err error) bool {
	if s == nil || s.modelCall == nil || s.modelCall.responsesRecoveryUsed || !strings.EqualFold(s.model.Protocol, modelresponses.Protocol) || !s.currentModelTurnRetrySafe() {
		return false
	}
	diagnostics, _ := modelErrorPayload(err)["diagnostics"].(map[string]any)
	if diagnostics == nil {
		return false
	}
	body, _ := diagnostics["upstreamBody"].(string)
	var rejection struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &rejection) != nil || rejection.Error.Code != "invalid_encrypted_content" {
		return false
	}
	// Strip only a completed conversation prefix. An active function/reasoning
	// group (including a restored HITL group) must retain its opaque state.
	boundary := -1
	for i, m := range s.messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && hasModelMessageContent(m.Content) {
			boundary = i
		}
	}
	changed := false
	messages := append([]openAIMessage(nil), s.messages...)
	for i := range messages {
		if len(messages[i].EncryptedReasoning) == 0 {
			continue
		}
		if i > boundary {
			return false
		}
		messages[i].EncryptedReasoning = nil
		changed = true
	}
	if !changed {
		return false
	}
	request, prepareErr := s.protocol.PrepareRequest(protocolStreamParams{runID: s.session.RunID, provider: s.provider, model: s.model, protocolConfig: s.protocolConfig, stageSettings: s.stageSettings, messages: messages, toolSpecs: s.toolSpecs, toolChoice: s.toolChoice})
	if prepareErr != nil {
		return false
	}
	if s.summaryCall {
		if s.prepareSummaryRequest(&request) != nil {
			return false
		}
	}
	call := s.modelCall
	call.responsesRecoveryUsed = true
	call.maxAttempts = max(call.maxAttempts, call.attempt+1)
	s.pending = append(s.pending, s.modelTurnDiscardDelta(call, err, true, call.attempt+1))
	s.closeCurrentProviderTurn()
	s.messages = messages
	call.prepared = request
	call.attempt++
	call.attemptStartedAt = time.Time{}
	s.pending = append(s.pending, s.buildLLMRequestDelta(request, call.effectiveToolChoice))
	return true
}

func hasModelMessageContent(content any) bool {
	switch v := content.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	default:
		raw, _ := json.Marshal(v)
		return len(raw) > 2 && string(raw) != "null"
	}
}
