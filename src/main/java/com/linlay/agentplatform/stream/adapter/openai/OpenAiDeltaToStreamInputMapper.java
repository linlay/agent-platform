package com.linlay.agentplatform.stream.adapter.openai;

import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.LlmDelta;
import com.linlay.agentplatform.stream.model.ToolCallDelta;
import reactor.core.publisher.Flux;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.atomic.AtomicLong;

public class OpenAiDeltaToStreamInputMapper {

    private final AtomicLong idCounter = new AtomicLong(0);
    private final Map<Integer, String> indexedToolIds = new HashMap<>();
    private String contentId;
    private String reasoningId;

    public List<StreamInput> mapOrEmpty(LlmDelta delta) {
        if (delta == null) {
            return List.of();
        }
        List<StreamInput> inputs = new ArrayList<>();

        if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
            for (int i = 0; i < delta.toolCalls().size(); i++) {
                ToolCallDelta toolCall = delta.toolCalls().get(i);
                String toolId = resolveToolId(toolCall, i);
                inputs.add(new StreamInput.ToolArgs(
                        toolId,
                        toolCall.arguments() == null ? "" : toolCall.arguments(),
                        null,
                        toolCall.name(),
                        toolCall.type(),
                        null,
                        null,
                        null,
                        null
                ));
            }
        }

        if (hasText(delta.reasoning())) {
            if (reasoningId == null) {
                reasoningId = "reasoning_" + idCounter.incrementAndGet();
            }
            inputs.add(new StreamInput.ReasoningDelta(reasoningId, delta.reasoning(), null));
        }

        if (hasText(delta.content())) {
            if (contentId == null) {
                contentId = "content_" + idCounter.incrementAndGet();
            }
            inputs.add(new StreamInput.ContentDelta(contentId, delta.content(), null));
        }

        if (hasText(delta.finishReason())) {
            inputs.add(new StreamInput.RunComplete(delta.finishReason()));
        }

        return inputs;
    }

    public Flux<StreamInput> map(Flux<LlmDelta> deltas) {
        Objects.requireNonNull(deltas, "deltas must not be null");
        return deltas.concatMapIterable(this::mapOrEmpty);
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

    private boolean hasText(String value) {
        return value != null && !value.isBlank();
    }
}
