package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwInput;
import com.aiagent.agw.sdk.model.AgwRequest;
import com.aiagent.agw.sdk.service.AgwEventAssembler;
import com.aiagent.agw.sdk.service.AgwSseStreamer;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.api.AgwQueryRequest;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * AGW 查询入口编排服务。
 * <p>
 * 负责把 API 请求组装为一次完整的运行上下文：选择 Agent、补齐 request/chat/run 标识、
 * 构建 AGW Query 请求、并将 AgentDelta 流映射为 AGW SSE 事件流。
 * 同时在流式发送过程中做事件规范化（如 plan.update）与会话持久化挂接。
 */
@Service
public class AgwQueryService {

    private static final String AUTO_AGENT = "auto";
    private static final String DEFAULT_AGENT = "default";
    private static final Pattern EVENT_TYPE_PATTERN = Pattern.compile("\"type\":\"([^\"]+)\"");
    private static final Logger log = LoggerFactory.getLogger(AgwQueryService.class);

    private final AgentRegistry agentRegistry;
    private final AgwSseStreamer agwSseStreamer;
    private final ObjectMapper objectMapper;
    private final ChatRecordStore chatRecordStore;
    private final ToolRegistry toolRegistry;

    public AgwQueryService(
            AgentRegistry agentRegistry,
            AgwSseStreamer agwSseStreamer,
            ObjectMapper objectMapper,
            ChatRecordStore chatRecordStore,
            ToolRegistry toolRegistry
    ) {
        this.agentRegistry = agentRegistry;
        this.agwSseStreamer = agwSseStreamer;
        this.objectMapper = objectMapper;
        this.chatRecordStore = chatRecordStore;
        this.toolRegistry = toolRegistry;
    }

    public QuerySession prepare(AgwQueryRequest request) {
        Agent agent = resolveAgent(request.agentKey());
        String chatId = parseOrGenerateUuid(request.chatId(), "chatId");
        String runId = UUID.randomUUID().toString();
        String requestId = StringUtils.hasText(request.requestId())
                ? request.requestId().trim()
                : runId;
        String role = StringUtils.hasText(request.role()) ? request.role().trim() : "user";
        Map<String, Object> querySnapshot = buildQuerySnapshot(request, requestId, chatId, role);
        ChatRecordStore.ChatSummary summary = chatRecordStore.ensureChat(chatId, agent.id(), request.message());
        String chatName = summary.chatName();
        Map<String, Object> queryParams = mergeQueryParams(request.params(), summary.created());
        AgwRequest.Query agwRequest = new AgwRequest.Query(
                requestId,
                chatId,
                role,
                request.message(),
                request.agentKey(),
                request.references() == null ? null : request.references().stream().map(value -> (Object) value).toList(),
                queryParams,
                serializeScene(request.scene()),
                request.stream(),
                chatName,
                runId
        );

        AgentRequest agentRequest = new AgentRequest(
                request.message(),
                chatId,
                requestId,
                runId,
                querySnapshot
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
        Flux<AgwInput> inputs = new AgentDeltaToAgwInputMapper(session.request().runId(), toolRegistry).map(deltas);
        return agwSseStreamer.stream(session.request(), inputs)
                .map(this::normalizePlanUpdateEvent)
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

    private ServerSentEvent<String> normalizePlanUpdateEvent(ServerSentEvent<String> event) {
        if (event == null || !StringUtils.hasText(event.data())) {
            return event;
        }
        JsonNode root;
        try {
            root = objectMapper.readTree(event.data());
        } catch (Exception ignored) {
            return event;
        }
        if (!"plan.update".equals(root.path("type").asText())) {
            return event;
        }
        ObjectNode normalized = objectMapper.createObjectNode();
        normalized.put("type", "plan.update");
        putIfPresent(normalized, "planId", root.get("planId"));
        putIfPresent(normalized, "chatId", root.get("chatId"));
        putIfPresent(normalized, "plan", root.get("plan"));
        putIfPresent(normalized, "timestamp", root.get("timestamp"));
        normalized.putNull("rawEvent");
        ServerSentEvent.Builder<String> builder = ServerSentEvent.builder(toJson(normalized));
        if (StringUtils.hasText(event.event())) {
            builder.event(event.event());
        }
        if (StringUtils.hasText(event.id())) {
            builder.id(event.id());
        }
        if (StringUtils.hasText(event.comment())) {
            builder.comment(event.comment());
        }
        if (event.retry() != null) {
            builder.retry(event.retry());
        }
        return builder.build();
    }

    private void putIfPresent(ObjectNode target, String key, JsonNode value) {
        if (target == null || key == null || value == null || value.isMissingNode()) {
            return;
        }
        target.set(key, value);
    }

    private String toJson(ObjectNode node) {
        try {
            return objectMapper.writeValueAsString(node);
        } catch (JsonProcessingException ex) {
            return "{\"type\":\"plan.update\"}";
        }
    }

    private Map<String, Object> mergeQueryParams(Map<String, Object> requestParams, boolean created) {
        Map<String, Object> merged = new LinkedHashMap<>();
        if (requestParams != null && !requestParams.isEmpty()) {
            merged.putAll(requestParams);
        }
        merged.put(AgwEventAssembler.INTERNAL_PARAM_EMIT_CHAT_START, created);
        return merged;
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

    private Map<String, Object> buildQuerySnapshot(
            AgwQueryRequest request,
            String requestId,
            String chatId,
            String role
    ) {
        Map<String, Object> snapshot = new LinkedHashMap<>();
        snapshot.put("requestId", requestId);
        snapshot.put("chatId", chatId);
        snapshot.put("agentKey", StringUtils.hasText(request.agentKey()) ? request.agentKey().trim() : null);
        snapshot.put("role", role);
        snapshot.put("message", request.message());
        snapshot.put("references", request.references());
        snapshot.put("params", request.params());
        snapshot.put("scene", request.scene());
        snapshot.put("stream", request.stream());
        return snapshot;
    }

    public record QuerySession(
            Agent agent,
            AgwRequest.Query request,
            AgentRequest agentRequest
    ) {
    }
}
