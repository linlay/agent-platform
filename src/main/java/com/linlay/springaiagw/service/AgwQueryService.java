package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwDelta;
import com.aiagent.agw.sdk.model.AgwRequestContext;
import com.aiagent.agw.sdk.service.AgwSseStreamer;
import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.agw.AgwQueryRequest;
import com.linlay.springaiagw.model.AgentDelta.ToolResult;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.SseChunk;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

import java.util.List;
import java.util.Map;
import java.util.UUID;

@Service
public class AgwQueryService {

    private static final String AUTO_AGENT = "auto";
    private static final String DEFAULT_AGENT = "default";

    private final AgentRegistry agentRegistry;
    private final AgwSseStreamer agwSseStreamer;

    public AgwQueryService(AgentRegistry agentRegistry, AgwSseStreamer agwSseStreamer) {
        this.agentRegistry = agentRegistry;
        this.agwSseStreamer = agwSseStreamer;
    }

    public QuerySession prepare(AgwQueryRequest request) {
        Agent agent = resolveAgent(request.agentKey());
        String chatId = parseOrGenerateUuid(request.chatId(), "chatId");
        String runId = UUID.randomUUID().toString();
        String requestId = StringUtils.hasText(request.requestId())
                ? request.requestId().trim()
                : runId;
        String chatName = chatId;

        AgwRequestContext context = new AgwRequestContext(
                request.message(),
                chatId,
                chatName,
                requestId,
                runId
        );

        AgentRequest agentRequest = new AgentRequest(
                request.message(),
                asStringParam(request.params(), "city"),
                asStringParam(request.params(), "date"),
                chatId,
                chatName,
                requestId,
                runId
        );
        return new QuerySession(agent, context, agentRequest);
    }

    public Flux<ServerSentEvent<String>> stream(QuerySession session) {
        Flux<AgwDelta> deltas = session.agent().stream(session.agentRequest()).map(this::toAgwDelta);
        return agwSseStreamer.stream(session.context(), deltas);
    }

    private Agent resolveAgent(String agentKey) {
        if (!StringUtils.hasText(agentKey)) {
            return agentRegistry.defaultAgent();
        }

        String normalized = agentKey.trim();
        if (AUTO_AGENT.equalsIgnoreCase(normalized) || DEFAULT_AGENT.equalsIgnoreCase(normalized)) {
            return agentRegistry.defaultAgent();
        }
        return agentRegistry.get(normalized);
    }

    private String asStringParam(Map<String, Object> params, String key) {
        if (params == null || !params.containsKey(key)) {
            return null;
        }
        Object value = params.get(key);
        return value == null ? null : String.valueOf(value);
    }

    private String parseOrGenerateUuid(String raw, String fieldName) {
        if (!StringUtils.hasText(raw)) {
            return UUID.randomUUID().toString();
        }
        try {
            return UUID.fromString(raw.trim()).toString();
        } catch (IllegalArgumentException ex) {
            throw new IllegalArgumentException(fieldName + " must be a valid UUID");
        }
    }

    private AgwDelta toAgwDelta(com.linlay.springaiagw.model.AgentDelta delta) {
        List<AgwDelta.ToolCall> toolCalls = delta.toolCalls() == null ? null : delta.toolCalls().stream()
                .map(this::toToolCall)
                .toList();
        List<AgwDelta.ToolResult> toolResults = delta.toolResults() == null ? null : delta.toolResults().stream()
                .map(this::toToolResult)
                .toList();

        return new AgwDelta(
                delta.content(),
                delta.thinking(),
                toolCalls,
                toolResults,
                delta.finishReason()
        );
    }

    private AgwDelta.ToolCall toToolCall(SseChunk.ToolCall toolCall) {
        String toolName = toolCall.function() == null ? null : toolCall.function().name();
        String arguments = toolCall.function() == null ? null : toolCall.function().arguments();
        return new AgwDelta.ToolCall(toolCall.id(), toolCall.type(), toolName, arguments);
    }

    private AgwDelta.ToolResult toToolResult(ToolResult toolResult) {
        return new AgwDelta.ToolResult(toolResult.toolId(), toolResult.result());
    }

    public record QuerySession(
            Agent agent,
            AgwRequestContext context,
            AgentRequest agentRequest
    ) {
    }
}
