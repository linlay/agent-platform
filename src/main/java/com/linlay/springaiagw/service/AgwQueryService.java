package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwInput;
import com.aiagent.agw.sdk.model.AgwRequest;
import com.aiagent.agw.sdk.service.AgwSseStreamer;
import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.agw.AgwQueryRequest;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.agw.AgentDelta;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

import java.util.List;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

@Service
public class AgwQueryService {

    private static final String AUTO_AGENT = "auto";
    private static final String DEFAULT_AGENT = "default";
    private static final String TOOL_RESULT_EVENT = "tool.result";
    private static final Pattern EVENT_TYPE_PATTERN = Pattern.compile("\"type\":\"([^\"]+)\"");
    private static final Logger log = LoggerFactory.getLogger(AgwQueryService.class);

    private final AgentRegistry agentRegistry;
    private final AgwSseStreamer agwSseStreamer;
    private final ObjectMapper objectMapper;
    private final ChatRecordStore chatRecordStore;

    public AgwQueryService(
            AgentRegistry agentRegistry,
            AgwSseStreamer agwSseStreamer,
            ObjectMapper objectMapper,
            ChatRecordStore chatRecordStore
    ) {
        this.agentRegistry = agentRegistry;
        this.agwSseStreamer = agwSseStreamer;
        this.objectMapper = objectMapper;
        this.chatRecordStore = chatRecordStore;
    }

    public QuerySession prepare(AgwQueryRequest request) {
        Agent agent = resolveAgent(request.agentKey());
        String chatId = parseOrGenerateUuid(request.chatId(), "chatId");
        String runId = UUID.randomUUID().toString();
        String requestId = StringUtils.hasText(request.requestId())
                ? request.requestId().trim()
                : runId;
        String role = StringUtils.hasText(request.role()) ? request.role().trim() : "user";
        ChatRecordStore.ChatSummary summary = chatRecordStore.ensureChat(chatId, agent.id(), request.message());
        String chatName = summary.chatName();
        AgwRequest.Query agwRequest = new AgwRequest.Query(
                requestId,
                chatId,
                role,
                request.message(),
                request.agentKey(),
                request.references() == null ? null : request.references().stream().map(value -> (Object) value).toList(),
                request.params(),
                serializeScene(request.scene()),
                request.stream(),
                chatName,
                runId
        );

        AgentRequest agentRequest = new AgentRequest(
                request.message(),
                chatId,
                requestId,
                runId
        );
        chatRecordStore.appendRequest(
                chatId,
                requestId,
                runId,
                agent.id(),
                request.message(),
                request.references(),
                request.scene()
        );
        return new QuerySession(agent, agwRequest, agentRequest);
    }

    public Flux<ServerSentEvent<String>> stream(QuerySession session) {
        Flux<AgentDelta> deltas = session.agent().stream(session.agentRequest());
        Flux<AgwInput> inputs = new AgentDeltaToAgwInputMapper(session.request().runId()).map(deltas);
        return agwSseStreamer.stream(session.request(), inputs)
                .map(this::normalizeToolResultPayload)
                .doOnNext(event -> {
                    String eventType = extractEventType(event.data());
                    if (!isToolEvent(eventType)) {
                        return;
                    }
                    log.debug(
                            "stream tool event type={}, requestId={}, runId={}",
                            eventType,
                            session.request().requestId(),
                            session.request().runId()
                    );
                })
                .doOnNext(event -> chatRecordStore.appendEvent(session.request().chatId(), event.data()));
    }

    private ServerSentEvent<String> normalizeToolResultPayload(ServerSentEvent<String> event) {
        if (event == null || !StringUtils.hasText(event.data())) {
            return event;
        }
        String eventType = extractEventType(event.data());
        if (!TOOL_RESULT_EVENT.equals(eventType)) {
            return event;
        }

        try {
            JsonNode root = objectMapper.readTree(event.data());
            if (!(root instanceof ObjectNode objectRoot)) {
                return event;
            }
            JsonNode resultNode = objectRoot.get("result");
            if (resultNode == null || !resultNode.isTextual()) {
                return event;
            }

            String rawResultText = resultNode.asText();
            JsonNode parsedResult = parseJson(rawResultText);
            if (parsedResult == null) {
                return event;
            }

            objectRoot.set("result", parsedResult);
            String normalizedData = objectMapper.writeValueAsString(objectRoot);

            ServerSentEvent.Builder<String> builder = ServerSentEvent.<String>builder()
                    .data(normalizedData);
            if (event.id() != null) {
                builder.id(event.id());
            }
            if (event.event() != null) {
                builder.event(event.event());
            }
            if (event.comment() != null) {
                builder.comment(event.comment());
            }
            if (event.retry() != null) {
                builder.retry(event.retry());
            }
            return builder.build();
        } catch (Exception ex) {
            log.debug("skip normalize tool.result payload due to parse failure", ex);
            return event;
        }
    }

    private JsonNode parseJson(String rawText) {
        if (!StringUtils.hasText(rawText)) {
            return null;
        }
        String trimmed = rawText.trim();
        if (!(trimmed.startsWith("{") || trimmed.startsWith("["))) {
            return null;
        }
        try {
            return objectMapper.readTree(trimmed);
        } catch (JsonProcessingException ex) {
            return null;
        }
    }

    private String extractEventType(String eventData) {
        if (!StringUtils.hasText(eventData)) {
            return null;
        }
        Matcher matcher = EVENT_TYPE_PATTERN.matcher(eventData);
        if (!matcher.find()) {
            return null;
        }
        return matcher.group(1);
    }

    private boolean isToolEvent(String eventType) {
        return eventType != null && eventType.startsWith("tool.");
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

    private String serializeScene(AgwQueryRequest.Scene scene) {
        if (scene == null) {
            return null;
        }
        try {
            return objectMapper.writeValueAsString(scene);
        } catch (JsonProcessingException ex) {
            return scene.toString();
        }
    }

    public record QuerySession(
            Agent agent,
            AgwRequest.Query request,
            AgentRequest agentRequest
    ) {
    }
}
