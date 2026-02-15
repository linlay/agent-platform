package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwInput;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.model.stream.AgentDelta;
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

public class AgentDeltaToAgwInputMapper {

    private final AtomicLong idCounter = new AtomicLong(0);
    private final Map<Integer, String> indexedToolIds = new HashMap<>();
    private final Map<String, AtomicInteger> toolArgChunkCounters = new HashMap<>();
    private final Map<String, StringBuilder> toolArgsBuffer = new HashMap<>();
    private final Set<String> actionToolIds = new HashSet<>();
    private final String reasoningId;
    private final String contentId;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper = new ObjectMapper();

    public AgentDeltaToAgwInputMapper() {
        this(null, null);
    }

    public AgentDeltaToAgwInputMapper(String runId) {
        this(runId, null);
    }

    public AgentDeltaToAgwInputMapper(String runId, ToolRegistry toolRegistry) {
        String prefix = hasText(runId) ? runId : "run";
        this.reasoningId = prefix + "_reasoning_1";
        this.contentId = prefix + "_content_1";
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

        List<AgwInput> inputs = new ArrayList<>();

        if (hasText(delta.reasoning())) {
            inputs.add(new AgwInput.ReasoningDelta(reasoningId, delta.reasoning(), null));
        }

        if (hasText(delta.content())) {
            inputs.add(new AgwInput.ContentDelta(contentId, delta.content(), null));
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

        return inputs;
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
