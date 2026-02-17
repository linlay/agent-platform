package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwInput;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.tool.ToolRegistry;
import reactor.core.publisher.Flux;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

/**
 * 将 Agent 内部流式增量（{@link AgentDelta}）转换为 AGW 输入事件序列。
 * <p>
 * 该类负责三类映射语义：
 * 1) 文本块（reasoning/content）按块分配独立 ID，块结束后不可复用；
 * 2) tool/action 增量参数与结果事件保持顺序并补齐必要元数据；
 * 3) 计划更新、运行结束等控制事件透传为对应 AGW 事件。
 */
public class AgentDeltaToAgwInputMapper {

    private final AtomicLong idCounter = new AtomicLong(0);
    private final Map<Integer, String> indexedToolIds = new HashMap<>();
    private final Map<String, AtomicInteger> toolArgChunkCounters = new HashMap<>();
    private final Map<String, StringBuilder> toolArgsBuffer = new HashMap<>();
    private final Set<String> actionToolIds = new HashSet<>();
    private final String runPrefix;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper = new ObjectMapper();

    private int reasoningSeq = 0;
    private int contentSeq = 0;
    private String activeReasoningId;
    private String activeContentId;

    public AgentDeltaToAgwInputMapper() {
        this(null, null);
    }

    public AgentDeltaToAgwInputMapper(String runId) {
        this(runId, null);
    }

    public AgentDeltaToAgwInputMapper(String runId, ToolRegistry toolRegistry) {
        this.runPrefix = hasText(runId) ? runId : "run";
        this.toolRegistry = toolRegistry;
    }

    public Flux<AgwInput> map(Flux<AgentDelta> deltas) {
        Objects.requireNonNull(deltas, "deltas must not be null");
        return deltas.concatMapIterable(this::mapOrEmpty);
    }

    public List<AgwInput> mapOrEmpty(AgentDelta delta) {
        if (delta == null) {
            return List.of();
        }

        if (hasText(delta.stageMarker())) {
            closeTextBlocks();
            return List.of();
        }

        List<AgwInput> inputs = new ArrayList<>();

        if (hasText(delta.reasoning())) {
            closeContentBlock();
            inputs.add(new AgwInput.ReasoningDelta(openReasoningBlockIfNeeded(), delta.reasoning(), null));
        }

        if (hasText(delta.content())) {
            closeReasoningBlock();
            inputs.add(new AgwInput.ContentDelta(openContentBlockIfNeeded(), delta.content(), null));
        }

        if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
            for (int i = 0; i < delta.toolCalls().size(); i++) {
                ToolCallDelta toolCall = delta.toolCalls().get(i);
                if (toolCall == null) {
                    continue;
                }
                String toolId = resolveToolId(toolCall, i);
                String toolName = toolCall.name();
                String normalizedType = resolveToolType(toolName, toolCall.type());
                String argsDelta = toolCall.arguments() == null ? "" : toolCall.arguments();
                toolArgsBuffer.computeIfAbsent(toolId, key -> new StringBuilder()).append(argsDelta);

                if ("action".equalsIgnoreCase(normalizedType)) {
                    actionToolIds.add(toolId);
                    inputs.add(new AgwInput.ActionArgs(
                            toolId,
                            argsDelta,
                            null,
                            toolName,
                            resolveDescription(toolName)
                    ));
                    continue;
                }

                int chunkIndex = toolArgChunkCounters
                        .computeIfAbsent(toolId, key -> new AtomicInteger(0))
                        .getAndIncrement();
                inputs.add(new AgwInput.ToolArgs(
                        toolId,
                        argsDelta,
                        null,
                        toolName,
                        normalizedType,
                        resolveToolApi(toolName),
                        parseToolParams(toolId),
                        resolveDescription(toolName),
                        chunkIndex
                ));
            }
        }

        if (delta.toolResults() != null && !delta.toolResults().isEmpty()) {
            for (AgentDelta.ToolResult toolResult : delta.toolResults()) {
                if (toolResult == null || !hasText(toolResult.toolId())) {
                    continue;
                }
                if (actionToolIds.contains(toolResult.toolId())) {
                    inputs.add(new AgwInput.ActionEnd(toolResult.toolId()));
                    inputs.add(new AgwInput.ActionResult(
                            toolResult.toolId(),
                            parseResult(toolResult.result())
                    ));
                    actionToolIds.remove(toolResult.toolId());
                    toolArgsBuffer.remove(toolResult.toolId());
                    continue;
                }
                inputs.add(new AgwInput.ToolResult(
                        toolResult.toolId(),
                        toolResult.result() == null ? "null" : toolResult.result()
                ));
                toolArgsBuffer.remove(toolResult.toolId());
            }
        }

        if (delta.planUpdate() != null) {
            AgentDelta.PlanUpdate planUpdate = delta.planUpdate();
            inputs.add(new AgwInput.PlanUpdate(
                    planUpdate.planId(),
                    planUpdate.plan(),
                    hasText(planUpdate.chatId()) ? planUpdate.chatId() : null
            ));
        }

        if (hasText(delta.finishReason())) {
            inputs.add(new AgwInput.RunComplete(delta.finishReason()));
        }

        if (hasNonTextPayload(delta)) {
            closeTextBlocks();
        }

        return inputs;
    }

    private boolean hasNonTextPayload(AgentDelta delta) {
        if (delta == null) {
            return false;
        }
        return (delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                || (delta.toolResults() != null && !delta.toolResults().isEmpty())
                || delta.planUpdate() != null
                || hasText(delta.finishReason());
    }

    private String openReasoningBlockIfNeeded() {
        if (!hasText(activeReasoningId)) {
            reasoningSeq++;
            activeReasoningId = runPrefix + "_reasoning_" + reasoningSeq;
        }
        return activeReasoningId;
    }

    private String openContentBlockIfNeeded() {
        if (!hasText(activeContentId)) {
            contentSeq++;
            activeContentId = runPrefix + "_content_" + contentSeq;
        }
        return activeContentId;
    }

    private void closeReasoningBlock() {
        activeReasoningId = null;
    }

    private void closeContentBlock() {
        activeContentId = null;
    }

    private void closeTextBlocks() {
        closeReasoningBlock();
        closeContentBlock();
    }

    private String resolveToolId(ToolCallDelta toolCall, int fallbackIndex) {
        if (hasText(toolCall.id())) {
            if (toolCall.index() != null) {
                indexedToolIds.put(toolCall.index(), toolCall.id());
            }
            return toolCall.id();
        }
        if (toolCall.index() != null && indexedToolIds.containsKey(toolCall.index())) {
            return indexedToolIds.get(toolCall.index());
        }
        int effectiveIndex = toolCall.index() != null ? toolCall.index() : fallbackIndex;
        String generated = "tool_" + idCounter.incrementAndGet();
        indexedToolIds.put(effectiveIndex, generated);
        return generated;
    }

    private String resolveToolType(String toolName, String rawType) {
        if (toolRegistry != null && hasText(toolName)) {
            return toolRegistry.toolCallType(toolName);
        }
        return hasText(rawType) ? rawType.trim() : "function";
    }

    private Object parseToolParams(String toolId) {
        StringBuilder builder = toolArgsBuffer.get(toolId);
        if (builder == null || builder.isEmpty()) {
            return null;
        }
        try {
            return objectMapper.readValue(builder.toString(), Object.class);
        } catch (Exception ignored) {
            return null;
        }
    }

    private Object parseResult(String raw) {
        String value = raw == null ? "null" : raw;
        try {
            return objectMapper.readValue(value, Object.class);
        } catch (Exception ignored) {
            return value;
        }
    }

    private String resolveDescription(String toolName) {
        if (toolRegistry == null || !hasText(toolName)) {
            return null;
        }
        String description = toolRegistry.description(toolName);
        return hasText(description) ? description : null;
    }

    private String resolveToolApi(String toolName) {
        if (toolRegistry == null || !hasText(toolName)) {
            return null;
        }
        return toolRegistry.capability(toolName)
                .map(descriptor -> hasText(descriptor.toolApi()) ? descriptor.toolApi() : null)
                .orElse(null);
    }

    private boolean hasText(String value) {
        return value != null && !value.isBlank();
    }
}
