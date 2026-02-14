package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.linlay.springaiagw.config.LlmInteractionLogProperties;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.util.StringUtils;

import java.util.List;
import java.util.Locale;

/**
 * LLM 调用日志工具：traceId 生成、delta 日志追加、耗时计算。
 */
class LlmCallLogger {

    private static final Logger log = LoggerFactory.getLogger(LlmCallLogger.class);

    private final boolean enabled;
    private final boolean maskSensitive;

    LlmCallLogger() {
        this.enabled = true;
        this.maskSensitive = true;
    }

    LlmCallLogger(LlmInteractionLogProperties properties) {
        this.enabled = properties == null || properties.isEnabled();
        this.maskSensitive = properties == null || properties.isMaskSensitive();
    }

    boolean isEnabled() {
        return enabled;
    }

    String generateTraceId() {
        return "llm-" + java.util.UUID.randomUUID().toString().replace("-", "");
    }

    long elapsedMs(long startNanos) {
        return (System.nanoTime() - startNanos) / 1_000_000;
    }

    String normalizePrompt(String prompt) {
        return sanitizeText(prompt == null ? "" : prompt);
    }

    String sanitizeText(String text) {
        return LlmLogSanitizer.maskText(text, maskSensitive);
    }

    void info(Logger logger, String pattern, Object... arguments) {
        if (enabled) {
            logger.info(pattern, arguments);
        }
    }

    void debug(Logger logger, String pattern, Object... arguments) {
        if (enabled) {
            logger.debug(pattern, arguments);
        }
    }

    void logHistoryMessages(Logger logger, String traceId, String stage, List<Message> historyMessages) {
        if (!enabled || historyMessages == null || historyMessages.isEmpty()) {
            return;
        }
        StringBuilder builder = new StringBuilder();
        for (int i = 0; i < historyMessages.size(); i++) {
            Message message = historyMessages.get(i);
            if (message == null) {
                continue;
            }
            builder.append('[').append(i).append("] role=")
                    .append(message.getMessageType() == null ? "unknown" : message.getMessageType().name().toLowerCase(Locale.ROOT))
                    .append(", text=")
                    .append(sanitizeText(message.getText()));

            if (message instanceof AssistantMessage assistantMessage
                    && assistantMessage.getToolCalls() != null
                    && !assistantMessage.getToolCalls().isEmpty()) {
                builder.append(", toolCalls=").append(assistantMessage.getToolCalls().size());
            }
            if (message instanceof ToolResponseMessage toolResponseMessage
                    && toolResponseMessage.getResponses() != null
                    && !toolResponseMessage.getResponses().isEmpty()) {
                builder.append(", toolResponses=").append(toolResponseMessage.getResponses().size());
            }
            builder.append('\n');
        }
        if (builder.length() == 0) {
            return;
        }
        logger.info("[{}][{}] LLM stream history messages detail:\n{}", traceId, stage, builder);
    }

    void appendDeltaLog(StringBuilder buffer, LlmDelta delta, String traceId, String stage) {
        if (!enabled || delta == null) {
            return;
        }
        if (delta.reasoning() != null && !delta.reasoning().isEmpty()) {
            String reasoning = sanitizeText(delta.reasoning());
            buffer.append("[reasoning] ").append(reasoning);
            log.debug("[{}][{}][delta] reasoning: {}", traceId, stage, reasoning);
        }
        if (delta.content() != null && !delta.content().isEmpty()) {
            String content = sanitizeText(delta.content());
            buffer.append(content);
            log.debug("[{}][{}][delta] content: {}", traceId, stage, content);
        }
        if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
            for (ToolCallDelta call : delta.toolCalls()) {
                if (call == null) {
                    continue;
                }
                String toolId = call.id() == null ? "" : call.id();
                String toolName = call.name() == null ? "" : call.name();
                String arguments = sanitizeText(call.arguments());
                buffer.append("\n[tool_call] id=").append(call.id())
                        .append(", name=").append(toolName)
                        .append(", args=").append(arguments);
                log.debug("[{}][{}][delta] tool_call id={}, name={}, args={}", traceId, stage,
                        toolId, toolName, arguments);
            }
        }
        if (StringUtils.hasText(delta.finishReason())) {
            String finishReason = sanitizeText(delta.finishReason());
            buffer.append("\n[finish_reason] ").append(finishReason);
            log.debug("[{}][{}][delta] finish_reason={}", traceId, stage, finishReason);
        }
    }
}
