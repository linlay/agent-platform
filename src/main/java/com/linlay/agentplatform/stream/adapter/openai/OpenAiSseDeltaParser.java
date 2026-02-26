package com.linlay.agentplatform.stream.adapter.openai;

import com.linlay.agentplatform.stream.model.LlmDelta;
import com.linlay.agentplatform.stream.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

public class OpenAiSseDeltaParser {

    private static final Logger log = LoggerFactory.getLogger(OpenAiSseDeltaParser.class);

    private final ObjectMapper objectMapper;

    public OpenAiSseDeltaParser(ObjectMapper objectMapper) {
        this.objectMapper = Objects.requireNonNull(objectMapper, "objectMapper cannot be null");
    }

    public LlmDelta parseOrNull(String rawChunk) {
        String payload = normalizePayload(rawChunk);
        if (payload == null) {
            return null;
        }
        try {
            JsonNode root = objectMapper.readTree(payload);
            Map<String, Object> usage = parseUsage(root.get("usage"));
            JsonNode choices = root.path("choices");
            if (!choices.isArray() || choices.isEmpty()) {
                if (usage != null && !usage.isEmpty()) {
                    return new LlmDelta(null, null, null, null, usage);
                }
                return null;
            }

            JsonNode firstChoice = choices.get(0);
            JsonNode deltaNode = firstChoice.path("delta");
            String reasoning = optionalText(deltaNode.get("reasoning_content"));
            String content = optionalText(deltaNode.get("content"));
            String finishReason = optionalText(firstChoice.get("finish_reason"));

            List<ToolCallDelta> toolCalls = new ArrayList<>();
            JsonNode toolCallsNode = deltaNode.path("tool_calls");
            if (toolCallsNode.isArray()) {
                for (JsonNode toolCallNode : toolCallsNode) {
                    String id = optionalText(toolCallNode.get("id"));
                    Integer index = optionalInt(toolCallNode.get("index"));
                    String type = optionalText(toolCallNode.get("type"));
                    JsonNode functionNode = toolCallNode.path("function");
                    String name = optionalText(functionNode.get("name"));
                    String arguments = optionalText(functionNode.get("arguments"));
                    if (!hasText(id) && index == null && !hasText(name) && !hasText(arguments)) {
                        continue;
                    }
                    toolCalls.add(new ToolCallDelta(id, index, type, name, arguments));
                }
            }

            boolean empty = !hasText(reasoning)
                    && !hasText(content)
                    && toolCalls.isEmpty()
                    && !hasText(finishReason)
                    && (usage == null || usage.isEmpty());
            if (empty) {
                return null;
            }
            return new LlmDelta(
                    reasoning,
                    content,
                    toolCalls.isEmpty() ? null : toolCalls,
                    finishReason,
                    usage
            );
        } catch (Exception ex) {
            log.warn("Failed to parse OpenAI SSE chunk: {}", rawChunk, ex);
            return null;
        }
    }

    private Map<String, Object> parseUsage(JsonNode usageNode) {
        if (usageNode == null || usageNode.isNull() || usageNode.isMissingNode() || !usageNode.isObject()) {
            return null;
        }
        Map<String, Object> usage = new LinkedHashMap<>();
        usageNode.fields().forEachRemaining(entry -> {
            JsonNode valueNode = entry.getValue();
            if (valueNode.isInt() || valueNode.isLong()) {
                usage.put(entry.getKey(), valueNode.asLong());
                return;
            }
            if (valueNode.isNumber()) {
                usage.put(entry.getKey(), valueNode.numberValue());
                return;
            }
            if (valueNode.isTextual()) {
                usage.put(entry.getKey(), valueNode.asText());
                return;
            }
            if (valueNode.isObject()) {
                usage.put(
                        entry.getKey(),
                        objectMapper.convertValue(valueNode, objectMapper.getTypeFactory()
                                .constructMapType(LinkedHashMap.class, String.class, Object.class))
                );
                return;
            }
            if (valueNode.isArray()) {
                usage.put(entry.getKey(), objectMapper.convertValue(valueNode, List.class));
                return;
            }
            if (!valueNode.isNull() && !valueNode.isMissingNode()) {
                usage.put(entry.getKey(), valueNode.toString());
            }
        });
        return usage.isEmpty() ? null : usage;
    }

    private String normalizePayload(String rawChunk) {
        if (!hasText(rawChunk)) {
            return null;
        }
        String payload = rawChunk.trim();
        if (payload.startsWith("data:")) {
            payload = payload.substring(5).trim();
        }
        if (!hasText(payload) || "[DONE]".equals(payload)) {
            return null;
        }
        return payload;
    }

    private String optionalText(JsonNode node) {
        if (node == null || node.isNull() || node.isMissingNode()) {
            return null;
        }
        if (node.isTextual()) {
            return node.asText();
        }
        return node.toString();
    }

    private Integer optionalInt(JsonNode node) {
        if (node == null || node.isNull() || node.isMissingNode()) {
            return null;
        }
        if (node.isInt() || node.isLong()) {
            return node.asInt();
        }
        if (node.isTextual()) {
            try {
                return Integer.parseInt(node.asText());
            } catch (NumberFormatException ignored) {
                return null;
            }
        }
        return null;
    }

    private boolean hasText(String text) {
        return text != null && !text.isBlank();
    }
}
