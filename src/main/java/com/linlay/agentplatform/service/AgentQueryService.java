package com.linlay.agentplatform.service;

import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.StreamRequest;
import com.linlay.agentplatform.stream.service.StreamEventAssembler;
import com.linlay.agentplatform.stream.service.StreamSseStreamer;
import com.linlay.agentplatform.config.FrontendToolProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.Agent;
import com.linlay.agentplatform.agent.AgentRegistry;
import com.linlay.agentplatform.model.api.QueryRequest;
import com.linlay.agentplatform.model.AgentRequest;
import com.linlay.agentplatform.model.AgentDelta;
import com.linlay.agentplatform.tool.CapabilityKind;
import com.linlay.agentplatform.tool.ToolRegistry;
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
 * Agent 查询入口编排服务。
 * <p>
 * 负责把 API 请求组装为一次完整的运行上下文：选择 Agent、补齐 request/chat/run 标识、
 * 构建 Query 请求、并将 AgentDelta 流映射为 SSE 事件流。
 * 同时在流式发送过程中做事件规范化（如 plan.update）与会话持久化挂接。
 */
@Service
public class AgentQueryService {

    private static final String AUTO_AGENT = "auto";
    private static final String DEFAULT_AGENT = "default";
    private static final Pattern EVENT_TYPE_PATTERN = Pattern.compile("\"type\":\"([^\"]+)\"");
    private static final Logger log = LoggerFactory.getLogger(AgentQueryService.class);

    private final AgentRegistry agentRegistry;
    private final StreamSseStreamer streamSseStreamer;
    private final ObjectMapper objectMapper;
    private final ChatRecordStore chatRecordStore;
    private final ToolRegistry toolRegistry;
    private final ViewportRegistryService viewportRegistryService;
    private final FrontendToolProperties frontendToolProperties;

    public AgentQueryService(
            AgentRegistry agentRegistry,
            StreamSseStreamer streamSseStreamer,
            ObjectMapper objectMapper,
            ChatRecordStore chatRecordStore,
            ToolRegistry toolRegistry,
            ViewportRegistryService viewportRegistryService,
            FrontendToolProperties frontendToolProperties
    ) {
        this.agentRegistry = agentRegistry;
        this.streamSseStreamer = streamSseStreamer;
        this.objectMapper = objectMapper;
        this.chatRecordStore = chatRecordStore;
        this.toolRegistry = toolRegistry;
        this.viewportRegistryService = viewportRegistryService;
        this.frontendToolProperties = frontendToolProperties;
    }

    public QuerySession prepare(QueryRequest request) {
        String chatId = parseOrGenerateUuid(request.chatId(), "chatId");
        String boundAgentKey = chatRecordStore.findBoundAgentKey(chatId).orElse(null);
        Agent agent = resolveAgent(StringUtils.hasText(boundAgentKey) ? boundAgentKey : request.agentKey());
        String runId = UUID.randomUUID().toString();
        String requestId = StringUtils.hasText(request.requestId())
                ? request.requestId().trim()
                : runId;
        String role = StringUtils.hasText(request.role()) ? request.role().trim() : "user";
        String effectiveAgentKey = agent.id();
        Map<String, Object> querySnapshot = buildQuerySnapshot(request, requestId, chatId, role, effectiveAgentKey);
        ChatRecordStore.ChatSummary summary = chatRecordStore.ensureChat(
                chatId,
                agent.id(),
                agent.name(),
                request.message()
        );
        String chatName = summary.chatName();
        Map<String, Object> queryParams = mergeQueryParams(request.params(), summary.created());
        StreamRequest.Query streamRequest = new StreamRequest.Query(
                requestId,
                chatId,
                role,
                request.message(),
                effectiveAgentKey,
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
        return new QuerySession(agent, streamRequest, agentRequest);
    }

    public Flux<ServerSentEvent<String>> stream(QuerySession session) {
        Flux<AgentDelta> deltas = session.agent().stream(session.agentRequest());
        Flux<StreamInput> inputs = new AgentDeltaToStreamInputMapper(session.request().runId(), toolRegistry).map(deltas);
        StringBuilder assistantContent = new StringBuilder();
        boolean[] completed = {false};
        return streamSseStreamer.stream(session.request(), inputs)
                .map(this::normalizeEvent)
                .doOnNext(event -> {
                    if (event == null || !StringUtils.hasText(event.data())) {
                        return;
                    }
                    JsonNode node;
                    try {
                        node = objectMapper.readTree(event.data());
                    } catch (Exception ignored) {
                        return;
                    }
                    String type = node.path("type").asText(null);
                    if ("content.delta".equals(type)) {
                        String delta = node.path("delta").asText("");
                        if (StringUtils.hasText(delta)) {
                            assistantContent.append(delta);
                        }
                    } else if ("content.snapshot".equals(type)) {
                        String text = node.path("text").asText("");
                        if (StringUtils.hasText(text)) {
                            assistantContent.setLength(0);
                            assistantContent.append(text);
                        }
                    } else if ("run.complete".equals(type) && !completed[0]) {
                        completed[0] = true;
                        long completedAt = node.path("timestamp").asLong(System.currentTimeMillis());
                        String assistantText = assistantContent.toString().trim();
                        chatRecordStore.onRunCompleted(new ChatRecordStore.RunCompletion(
                                session.request().chatId(),
                                session.request().runId(),
                                StringUtils.hasText(assistantText) ? assistantText : null,
                                session.request().message(),
                                completedAt
                        ));
                    }
                })
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

    private ServerSentEvent<String> normalizeEvent(ServerSentEvent<String> event) {
        if (event == null) {
            return null;
        }

        ServerSentEvent<String> heartbeatNormalized = normalizeHeartbeatCommentEvent(event);
        if (heartbeatNormalized != event) {
            return heartbeatNormalized;
        }

        if (!StringUtils.hasText(event.data())) {
            return event;
        }
        JsonNode root;
        try {
            root = objectMapper.readTree(event.data());
        } catch (Exception ignored) {
            return event;
        }
        if (!(root instanceof ObjectNode objectNode)) {
            return event;
        }

        String type = objectNode.path("type").asText();
        if ("plan.update".equals(type)) {
            ObjectNode normalized = objectMapper.createObjectNode();
            normalized.put("type", "plan.update");
            putIfPresent(normalized, "planId", objectNode.get("planId"));
            putIfPresent(normalized, "chatId", objectNode.get("chatId"));
            putIfPresent(normalized, "plan", objectNode.get("plan"));
            putIfPresent(normalized, "timestamp", objectNode.get("timestamp"));
            return rebuildEvent(event, normalized);
        }

        if (normalizeFrontendToolEvent(type, objectNode)) {
            return rebuildEvent(event, objectNode);
        }

        return event;
    }

    private ServerSentEvent<String> normalizeHeartbeatCommentEvent(ServerSentEvent<String> event) {
        if (!StringUtils.hasText(event.comment())
                || StringUtils.hasText(event.event())
                || StringUtils.hasText(event.data())) {
            return event;
        }
        if (!"heartbeat".equals(event.comment().trim())) {
            return event;
        }

        ServerSentEvent.Builder<String> builder = ServerSentEvent.builder();
        builder.event("heartbeat");
        if (StringUtils.hasText(event.id())) {
            builder.id(event.id());
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
            return "{}";
        }
    }

    private ServerSentEvent<String> rebuildEvent(ServerSentEvent<String> original, ObjectNode data) {
        ServerSentEvent.Builder<String> builder = ServerSentEvent.builder(toJson(data));
        if (StringUtils.hasText(original.event())) {
            builder.event(original.event());
        }
        if (StringUtils.hasText(original.id())) {
            builder.id(original.id());
        }
        if (StringUtils.hasText(original.comment())) {
            builder.comment(original.comment());
        }
        if (original.retry() != null) {
            builder.retry(original.retry());
        }
        return builder.build();
    }

    private boolean normalizeFrontendToolEvent(String eventType, ObjectNode root) {
        if (!"tool.start".equals(eventType) && !"tool.snapshot".equals(eventType)) {
            return false;
        }

        String toolName = root.path("toolName").asText(null);
        if (!StringUtils.hasText(toolName)) {
            return false;
        }

        return toolRegistry.capability(toolName)
                .filter(descriptor -> descriptor.kind() == CapabilityKind.FRONTEND)
                .map(descriptor -> {
                    String toolKey = StringUtils.hasText(descriptor.viewportKey())
                            ? descriptor.viewportKey().trim()
                            : null;
                    if (!StringUtils.hasText(toolKey)) {
                        return false;
                    }
                    root.put("toolKey", toolKey);
                    root.put("toolType", resolveFrontendToolType(toolKey));
                    root.put("toolTimeout", Math.max(1L, frontendToolProperties.getSubmitTimeoutMs()));
                    return true;
                })
                .orElse(false);
    }

    private String resolveFrontendToolType(String toolKey) {
        return viewportRegistryService.find(toolKey)
                .map(viewport -> viewport.viewportType().value())
                .filter(StringUtils::hasText)
                .orElse("html");
    }

    private Map<String, Object> mergeQueryParams(Map<String, Object> requestParams, boolean created) {
        Map<String, Object> merged = new LinkedHashMap<>();
        if (requestParams != null && !requestParams.isEmpty()) {
            merged.putAll(requestParams);
        }
        merged.put(StreamEventAssembler.INTERNAL_PARAM_EMIT_CHAT_START, created);
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

    private String serializeScene(QueryRequest.Scene scene) {
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
            QueryRequest request,
            String requestId,
            String chatId,
            String role,
            String effectiveAgentKey
    ) {
        Map<String, Object> snapshot = new LinkedHashMap<>();
        snapshot.put("requestId", requestId);
        snapshot.put("chatId", chatId);
        snapshot.put("agentKey", effectiveAgentKey);
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
            StreamRequest.Query request,
            AgentRequest agentRequest
    ) {
    }
}
