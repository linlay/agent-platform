package llm

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"agent-platform/internal/modelclient"
	"agent-platform/internal/observability"
)

// Keep counters rather than raw frames: diagnostics must remain bounded and
// must not add prompts, reasoning text, tool arguments or credentials to logs.
type providerStreamObservation struct {
	Response               modelclient.ResponseMetadata `json:"response"`
	Frames                 int                          `json:"frames"`
	DataBytes              int                          `json:"dataBytes"`
	DoneSeen               bool                         `json:"doneSeen"`
	ReadOutcome            string                       `json:"readOutcome,omitempty"`
	CompletionTrigger      string                       `json:"completionTrigger,omitempty"`
	DecodeErrors           int                          `json:"decodeErrors"`
	RawContentBytes        int                          `json:"rawContentBytes"`
	RawReasoningBytes      int                          `json:"rawReasoningBytes"`
	RawToolDeltas          int                          `json:"rawToolDeltas"`
	RefusalBytes           int                          `json:"refusalBytes"`
	NonStreamingMessages   int                          `json:"nonStreamingMessages"`
	IgnoredAnthropicEvents int                          `json:"ignoredAnthropicEvents"`
}

func (o *providerStreamObservation) recordRead(raw string, err error) {
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			o.ReadOutcome = "eof"
		case isProviderTimeoutError(err):
			o.ReadOutcome = "timeout"
		default:
			o.ReadOutcome = "read_error"
		}
		return
	}
	o.Frames++
	o.DataBytes += len(raw)
	if raw == "[DONE]" {
		o.DoneSeen = true
	}
}

func (o *providerStreamObservation) recordOpenAIChunk(decoded openAIStreamResponse) {
	for _, choice := range decoded.Choices {
		o.RawContentBytes += len(choice.Delta.Content)
		o.RawReasoningBytes += len(choice.Delta.ReasoningContent) + len(choice.Delta.Reasoning)
		for _, text := range extractReasoningDetailTexts(choice.Delta.ReasoningDetails) {
			o.RawReasoningBytes += len(text)
		}
		o.RawToolDeltas += len(choice.Delta.ToolCalls)
		var refusal string
		if json.Unmarshal(choice.Delta.Refusal, &refusal) == nil {
			o.RefusalBytes += len(refusal)
		}
		if len(choice.Message) > 0 && string(choice.Message) != "null" {
			o.NonStreamingMessages++
		}
	}
}

func diagnosticLabel(value string) string {
	value = observability.SanitizeLog(strings.TrimSpace(value))
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func (s *llmRunStream) observeModelAttempt(turn *providerTurnStream, trace *llmChatTrace, err error) {
	d := map[string]any{
		"chatId": diagnosticLabel(s.session.ChatID), "runId": diagnosticLabel(s.session.RunID),
		"requestId": diagnosticLabel(s.session.RequestID), "agentKey": diagnosticLabel(s.session.AgentKey),
		"providerKey": diagnosticLabel(s.provider.Key), "modelKey": diagnosticLabel(s.model.Key),
		"modelId": diagnosticLabel(s.model.ModelID), "protocol": diagnosticLabel(s.model.Protocol),
		"stage": diagnosticLabel(s.promptBuildOptions.Stage), "runSeq": s.runLLMChatCompletionCount,
		"attempt": 1, "maxAttempts": 1, "reasoningFormat": s.responseReasoningFormat(),
		"reasoningEffort": s.effectiveReasoningEffort(), "emptyResponse": false,
	}
	if d["protocol"] == "" {
		d["protocol"] = "OPENAI"
	}
	if call := s.modelCall; call != nil {
		d["runSeq"], d["attempt"], d["maxAttempts"] = call.runSeq, call.attempt, call.maxAttempts
		d["durationMs"] = modelActivityElapsedMs(call.attemptStartedAt)
		// Only numeric output limits from the actual prepared request are safe.
		for _, key := range []string{"max_tokens", "max_completion_tokens"} {
			if value, ok := call.prepared.RequestBody[key].(float64); ok {
				d[key] = value
			}
		}
	}
	category := ""
	if turn != nil {
		d["stream"] = turn.observation
		d["finishReason"] = diagnosticLabel(turn.finishReason)
		d["contentBytes"], d["reasoningBytes"], d["toolCalls"] = turn.content.Len(), turn.reasoning.Len(), len(turn.toolCalls)
		if !turn.requestSentAt.IsZero() {
			d["durationMs"] = time.Since(turn.requestSentAt).Milliseconds()
		}
		if turn.usage != nil {
			d["usage"] = map[string]any{
				"promptTokens": turn.usage.PromptTokens, "completionTokens": turn.usage.CompletionTokens,
				"totalTokens": turn.usage.TotalTokens, "reasoningTokens": turn.usage.CompletionTokensDetails.ReasoningTokens,
			}
		}
		if err == nil && strings.TrimSpace(turn.content.String()) == "" && len(turn.toolCalls) == 0 {
			d["emptyResponse"] = true
			d["reason"] = emptyResponseReason(turn)
			category = "llm_empty_response"
		}
	}
	if err != nil {
		payload := modelErrorPayload(err)
		d["errorCode"] = payload["code"]
		// Error bodies may contain request data; retain only the HTTP status.
		if details, ok := payload["diagnostics"].(map[string]any); ok {
			if status, ok := details["upstreamStatus"]; ok {
				d["httpStatus"] = status
			}
		}
		category = "llm_model_attempt_error"
	}
	if trace != nil {
		trace.setDiagnostics(d)
	}
	if category != "" {
		// Anomaly summaries are always enabled, even when raw/trace logging is off.
		observability.Log(category, d)
	}
}

// These labels describe observed signals, not a proven upstream root cause.
func emptyResponseReason(turn *providerTurnStream) string {
	switch strings.ToLower(strings.TrimSpace(turn.finishReason)) {
	case "length", "max_tokens":
		return "output_limit"
	case "content_filter", "refusal":
		return "content_filter_or_refusal"
	case "tool_calls", "tool_use":
		return "tool_calls_missing"
	}
	if turn.observation.RefusalBytes > 0 {
		return "content_filter_or_refusal"
	}
	if turn.observation.NonStreamingMessages > 0 {
		return "non_streaming_message"
	}
	if turn.reasoning.Len() > 0 || turn.observation.RawReasoningBytes > 0 {
		return "reasoning_only"
	}
	if strings.TrimSpace(turn.finishReason) == "" {
		return "missing_finish_reason"
	}
	return "empty_stop"
}
