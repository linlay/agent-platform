package llm

import (
	"strings"

	"agent-platform/internal/apperrors"
)

func modelOutputLimited(turn *providerTurnStream) bool {
	switch strings.ToLower(strings.TrimSpace(turn.finishReason)) {
	case "length", "max_tokens":
		return true
	}
	return false
}

// Classify observed termination signals without guessing which upstream layer
// imposed a limit. These terminal failures must not retry or execute tools.
func modelResponseFailure(turn *providerTurnStream) map[string]any {
	empty := strings.TrimSpace(turn.content.String()) == ""
	filtered := turn.observation.RefusalBytes > 0
	switch strings.ToLower(strings.TrimSpace(turn.finishReason)) {
	case "content_filter", "refusal":
		filtered = true
	}
	if !modelOutputLimited(turn) && !filtered && (!empty || len(turn.toolCalls) > 0) {
		return nil
	}
	reason := emptyResponseReason(turn)
	code := apperrors.CodeModelEmptyResponse
	message := "模型已结束生成，但没有返回回答或工具调用。任务未完成，请重试或切换模型。"
	switch {
	case modelOutputLimited(turn):
		code = apperrors.CodeModelOutputLimit
		message = "本轮输出额度已用尽，任务未完成。请调整模型或中转服务的输出额度后重试。"
		if !empty {
			message = "本轮输出额度已用尽，回答可能不完整，任务未完成。已生成的内容和此前已完成的操作已保留。"
		} else if turn.reasoning.Len() > 0 || turn.observation.RawReasoningBytes > 0 {
			message = "本轮输出额度已用尽，仅生成了思考内容，尚未产生回答或执行本轮工具。任务未完成。"
		}
	case filtered:
		code = apperrors.CodeProviderContentFilter
		message = "模型服务返回了内容过滤或拒绝信号，任务未完成。请调整请求后重试。"
	default:
		switch reason {
		case "reasoning_only":
			message = "模型仅返回了思考内容，没有产生回答或工具调用。任务未完成，请重试或切换模型。"
		case "tool_calls_missing":
			message = "模型报告需要调用工具，但没有返回工具调用数据。任务未完成，请重试或检查模型协议兼容配置。"
		case "non_streaming_message":
			message = "模型服务返回了非流式消息，平台未能从流式响应中读取回答。任务未完成，请检查模型服务的流式协议兼容性。"
		case "missing_finish_reason":
			message = "模型响应已结束，但没有返回回答、工具调用或结束原因。任务未完成，请重试或检查模型服务。"
		}
	}
	payload := apperrors.Payload(code, message)
	payload["diagnostics"] = map[string]any{"reason": reason, "finishReason": diagnosticLabel(turn.finishReason)}
	return payload
}
