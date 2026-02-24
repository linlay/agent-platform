package com.linlay.agentplatform.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.memory.ChatWindowMemoryProperties;
import com.linlay.agentplatform.memory.ChatWindowMemoryStore;
import com.linlay.agentplatform.model.api.ChatDetailResponse;
import com.linlay.agentplatform.model.api.ChatSummaryResponse;
import com.linlay.agentplatform.model.api.QueryRequest;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

import java.io.IOException;
import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardOpenOption;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

/**
 * 会话记录读取与快照回放服务。
 * <p>
 * 负责维护聊天索引（会话列表元数据）、读取运行历史文件、并将持久化消息转换为
 * 前端可消费的事件快照与原始消息列表。该类不参与实时 SSE 发送，仅处理存储侧视图重建。
 */
@Service
public class ChatRecordStore {

    private static final Logger log = LoggerFactory.getLogger(ChatRecordStore.class);
    private static final String CHAT_INDEX_FILE = "_chats.jsonl";

    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryProperties properties;
    private final Object lock = new Object();

    public ChatRecordStore(ObjectMapper objectMapper, ChatWindowMemoryProperties properties) {
        this.objectMapper = objectMapper;
        this.properties = properties;
    }

    public ChatSummary ensureChat(String chatId, String firstAgentKey, String firstAgentName, String firstMessage) {
        requireValidChatId(chatId);
        synchronized (lock) {
            LinkedHashMap<String, ChatIndexRecord> recordsByChatId = new LinkedHashMap<>();
            for (ChatIndexRecord record : readIndexRecords()) {
                recordsByChatId.put(record.chatId, record);
            }
            String normalizedFirstAgentKey = nullable(firstAgentKey);
            String normalizedFirstAgentName = resolveFirstAgentName(firstAgentName, normalizedFirstAgentKey);
            long now = System.currentTimeMillis();
            ChatIndexRecord record = recordsByChatId.get(chatId);
            boolean created = false;
            if (record == null) {
                record = new ChatIndexRecord();
                record.chatId = chatId;
                record.chatName = deriveChatName(firstMessage);
                record.firstAgentKey = normalizedFirstAgentKey;
                record.firstAgentName = normalizedFirstAgentName;
                record.createdAt = now;
                record.updatedAt = now;
                recordsByChatId.put(chatId, record);
                created = true;
            } else {
                if (!StringUtils.hasText(record.chatName)) {
                    record.chatName = deriveChatName(firstMessage);
                }
                if (!StringUtils.hasText(record.firstAgentKey)) {
                    record.firstAgentKey = normalizedFirstAgentKey;
                }
                if (!StringUtils.hasText(record.firstAgentName)) {
                    record.firstAgentName = resolveFirstAgentName(normalizedFirstAgentName, record.firstAgentKey);
                }
                if (record.createdAt <= 0) {
                    record.createdAt = now;
                }
                record.updatedAt = now;
            }
            writeIndexRecords(List.copyOf(recordsByChatId.values()));
            return toChatSummary(record, created);
        }
    }

    public void appendRequest(
            String chatId,
            String requestId,
            String runId,
            String agentKey,
            String message,
            List<QueryRequest.Reference> references,
            QueryRequest.Scene scene
    ) {
        // request metadata is persisted in memory run.query
    }

    public void appendEvent(String chatId, String eventData) {
        // events are emitted over SSE; no event append file in current design.
    }

    public List<ChatSummaryResponse> listChats() {
        synchronized (lock) {
            return readIndexRecords().stream()
                    .sorted((left, right) -> Long.compare(resolveUpdatedAt(right), resolveUpdatedAt(left)))
                    .map(this::toChatSummary)
                    .map(summary -> new ChatSummaryResponse(
                            summary.chatId(),
                            summary.chatName(),
                            summary.firstAgentKey(),
                            summary.firstAgentName(),
                            summary.createdAt(),
                            summary.updatedAt()
                    ))
                    .toList();
        }
    }

    public ChatDetailResponse loadChat(String chatId, boolean includeRawMessages) {
        requireValidChatId(chatId);
        Path historyPath = resolveHistoryPath(chatId);
        synchronized (lock) {
            Optional<ChatIndexRecord> indexRecord = readIndexRecords().stream()
                    .filter(item -> chatId.equals(item.chatId))
                    .findFirst();

            if (indexRecord.isEmpty() && !Files.exists(historyPath)) {
                throw new ChatNotFoundException(chatId);
            }

            ChatSummary summary = indexRecord
                    .map(this::toChatSummary)
                    .orElseGet(() -> {
                        long createdAt = resolveCreatedAt(historyPath);
                        return new ChatSummary(chatId, chatId, null, null, createdAt, createdAt, false);
                    });

            ParsedChatContent content = readChatContent(
                    historyPath,
                    summary.chatId,
                    summary.chatName
            );

            List<Map<String, Object>> events = List.copyOf(content.events);
            List<Map<String, Object>> rawMessages = includeRawMessages
                    ? List.copyOf(content.rawMessages)
                    : null;
            List<QueryRequest.Reference> references = content.references.isEmpty()
                    ? null
                    : List.copyOf(content.references.values());

            return new ChatDetailResponse(
                    summary.chatId,
                    summary.chatName,
                    null,
                    rawMessages,
                    events,
                    references
            );
        }
    }

    private ParsedChatContent readChatContent(
            Path historyPath,
            String chatId,
            String chatName
    ) {
        ParsedChatContent content = new ParsedChatContent();
        readHistoryLines(historyPath, content);

        content.runs.sort(
                Comparator.comparingLong(this::sortByUpdatedAt)
                        .thenComparingInt(RunSnapshot::lineIndex)
        );

        for (RunSnapshot run : content.runs) {
            for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
                Map<String, Object> raw = toRawMessageMap(run.runId, message);
                if (!raw.isEmpty()) {
                    content.rawMessages.add(raw);
                }
            }
        }
        content.events.addAll(buildSnapshotEvents(chatId, chatName, content.runs));
        return content;
    }

    private void readHistoryLines(Path historyPath, ParsedChatContent content) {
        if (!Files.exists(historyPath)) {
            return;
        }
        try {
            List<String> lines = Files.readAllLines(historyPath, resolveCharset());
            // Intermediate structures to group by runId
            LinkedHashMap<String, Map<String, Object>> queryByRunId = new LinkedHashMap<>();
            LinkedHashMap<String, List<StepEntry>> stepsByRunId = new LinkedHashMap<>();
            int lineIndex = 0;

            for (String line : lines) {
                if (!StringUtils.hasText(line)) {
                    lineIndex++;
                    continue;
                }

                JsonNode node = parseLine(line);
                if (node == null || !node.isObject()) {
                    lineIndex++;
                    continue;
                }

                String type = node.path("_type").asText("");
                String runId = node.path("runId").asText(null);
                if (!StringUtils.hasText(runId)) {
                    lineIndex++;
                    continue;
                }

                if ("query".equals(type)) {
                    Map<String, Object> query = new LinkedHashMap<>();
                    if (node.has("query") && node.get("query").isObject()) {
                        query = objectMapper.convertValue(
                                node.get("query"),
                                objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class)
                        );
                    }
                    queryByRunId.put(runId, query);
                    collectReferencesFromQuery(query, content);
                    // Ensure run order
                    stepsByRunId.computeIfAbsent(runId, k -> new ArrayList<>());
                } else if ("step".equals(type)) {
                    long updatedAt = node.path("updatedAt").asLong(0);
                    String stage = node.path("_stage").asText(null);
                    int seq = node.path("_seq").asInt(0);
                    String taskId = node.has("taskId") && !node.get("taskId").isNull()
                            ? node.path("taskId").asText(null)
                            : null;

                    ChatWindowMemoryStore.SystemSnapshot system = null;
                    if (node.has("system") && !node.get("system").isNull()) {
                        system = objectMapper.treeToValue(node.get("system"), ChatWindowMemoryStore.SystemSnapshot.class);
                    }

                    ChatWindowMemoryStore.PlanSnapshot plan = null;
                    JsonNode planNode = node.has("plan") && !node.get("plan").isNull()
                            ? node.get("plan")
                            : (node.has("planSnapshot") && !node.get("planSnapshot").isNull() ? node.get("planSnapshot") : null);
                    if (planNode != null) {
                        plan = objectMapper.treeToValue(planNode, ChatWindowMemoryStore.PlanSnapshot.class);
                    }

                    List<ChatWindowMemoryStore.StoredMessage> messages = new ArrayList<>();
                    if (node.has("messages") && node.get("messages").isArray()) {
                        for (JsonNode msgNode : node.get("messages")) {
                            ChatWindowMemoryStore.StoredMessage msg = objectMapper.treeToValue(msgNode, ChatWindowMemoryStore.StoredMessage.class);
                            if (msg != null) {
                                messages.add(msg);
                            }
                        }
                    }

                    stepsByRunId.computeIfAbsent(runId, k -> new ArrayList<>())
                            .add(new StepEntry(stage, seq, taskId, updatedAt, system, plan, messages, lineIndex));
                }
                lineIndex++;
            }

            // Build RunSnapshots from grouped data
            int runIndex = 0;
            for (Map.Entry<String, List<StepEntry>> entry : stepsByRunId.entrySet()) {
                String runId = entry.getKey();
                List<StepEntry> steps = entry.getValue();
                if (steps.isEmpty()) {
                    runIndex++;
                    continue;
                }

                steps.sort(Comparator.comparingInt(s -> s.seq));

                Map<String, Object> query = queryByRunId.getOrDefault(runId, Map.of());
                long updatedAt = steps.stream().mapToLong(s -> s.updatedAt).max().orElse(0);

                // Flatten all step messages
                List<ChatWindowMemoryStore.StoredMessage> allMessages = new ArrayList<>();
                ChatWindowMemoryStore.SystemSnapshot firstSystem = null;
                ChatWindowMemoryStore.PlanSnapshot latestPlan = null;
                for (StepEntry step : steps) {
                    if (firstSystem == null && step.system != null) {
                        firstSystem = step.system;
                    }
                    if (step.plan != null) {
                        latestPlan = step.plan;
                    }
                    allMessages.addAll(step.messages);
                }

                int firstLineIndex = steps.getFirst().lineIndex;
                content.runs.add(new RunSnapshot(
                        runId,
                        updatedAt,
                        query,
                        firstSystem,
                        latestPlan,
                        List.copyOf(allMessages),
                        firstLineIndex
                ));
                runIndex++;
            }
        } catch (Exception ex) {
            log.warn("Cannot read chat history file={}, fallback to empty", historyPath, ex);
        }
    }

    private void collectReferencesFromQuery(Map<String, Object> query, ParsedChatContent content) {
        if (query == null || query.isEmpty()) {
            return;
        }
        Object referencesObject = query.get("references");
        if (!(referencesObject instanceof List<?> referencesList)) {
            return;
        }

        for (Object item : referencesList) {
            if (item == null) {
                continue;
            }
            try {
                QueryRequest.Reference reference = objectMapper.convertValue(item, QueryRequest.Reference.class);
                if (reference == null || !StringUtils.hasText(reference.id())) {
                    continue;
                }
                content.references.putIfAbsent(reference.id().trim(), reference);
            } catch (Exception ignored) {
                // ignore invalid reference item
            }
        }
    }

    private List<Map<String, Object>> buildSnapshotEvents(
            String chatId,
            String chatName,
            List<RunSnapshot> runs
    ) {
        if (runs.isEmpty()) {
            return List.of();
        }

        List<Map<String, Object>> events = new ArrayList<>();
        long seq = 1L;
        boolean emittedChatStart = false;

        for (RunSnapshot run : runs) {
            long runStartTs = resolveRunStartTimestamp(run);
            long runEndTs = resolveRunEndTimestamp(run, runStartTs);
            long timestampCursor = runStartTs;
            int reasoningIndex = 0;
            int contentIndex = 0;
            int toolIndex = 0;
            int actionIndex = 0;
            Map<String, IdBinding> bindingByCallId = new LinkedHashMap<>();

            Map<String, Object> requestQueryPayload = buildRequestQueryPayload(chatId, run);
            events.add(event("request.query", timestampCursor, seq++, requestQueryPayload));

            if (!emittedChatStart) {
                timestampCursor = normalizeEventTimestamp(timestampCursor + 1, timestampCursor);
                Map<String, Object> chatStartPayload = new LinkedHashMap<>();
                chatStartPayload.put("chatId", chatId);
                if (StringUtils.hasText(chatName)) {
                    chatStartPayload.put("chatName", chatName);
                }
                events.add(event("chat.start", timestampCursor, seq++, chatStartPayload));
                emittedChatStart = true;
            }

            timestampCursor = normalizeEventTimestamp(timestampCursor + 1, timestampCursor);
            Map<String, Object> runStartPayload = new LinkedHashMap<>();
            runStartPayload.put("runId", run.runId);
            runStartPayload.put("chatId", chatId);
            events.add(event("run.start", timestampCursor, seq++, runStartPayload));

            Map<String, Object> planUpdate = planUpdateEvent(run.plan, chatId, timestampCursor);
            if (!planUpdate.isEmpty()) {
                timestampCursor = normalizeEventTimestamp(((Number) planUpdate.get("timestamp")).longValue(), timestampCursor);
                events.add(planUpdate);
            }

            for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
                if (message == null || !StringUtils.hasText(message.role)) {
                    continue;
                }
                long messageTs = normalizeEventTimestamp(resolveMessageTimestamp(message, timestampCursor), timestampCursor);
                String role = message.role.trim().toLowerCase();

                if ("assistant".equals(role)) {
                    if (message.reasoningContent != null && !message.reasoningContent.isEmpty()) {
                        String text = textFromContent(message.reasoningContent);
                        if (StringUtils.hasText(text)) {
                            Map<String, Object> payload = new LinkedHashMap<>();
                            payload.put("reasoningId", StringUtils.hasText(message.reasoningId)
                                    ? message.reasoningId
                                    : run.runId + "_r_" + reasoningIndex++);
                            payload.put("text", text);
                            events.add(event("reasoning.snapshot", messageTs, seq++, payload));
                            timestampCursor = messageTs;
                        }
                    }
                    if (message.content != null && !message.content.isEmpty()) {
                        String text = textFromContent(message.content);
                        if (StringUtils.hasText(text)) {
                            Map<String, Object> payload = new LinkedHashMap<>();
                            payload.put("contentId", StringUtils.hasText(message.contentId)
                                    ? message.contentId
                                    : run.runId + "_c_" + contentIndex++);
                            payload.put("text", text);
                            events.add(event("content.snapshot", messageTs, seq++, payload));
                            timestampCursor = messageTs;
                        }
                    }
                    if (message.toolCalls != null && !message.toolCalls.isEmpty()) {
                        for (ChatWindowMemoryStore.StoredToolCall toolCall : message.toolCalls) {
                            if (toolCall == null || toolCall.function == null || !StringUtils.hasText(toolCall.function.name)) {
                                continue;
                            }
                            IdBinding binding = resolveBindingForAssistantToolCall(run.runId, message, toolCall, toolIndex, actionIndex);
                            if (binding.action) {
                                actionIndex++;
                            } else {
                                toolIndex++;
                            }
                            if (StringUtils.hasText(toolCall.id)) {
                                bindingByCallId.put(toolCall.id.trim(), binding);
                            }

                            Map<String, Object> payload = new LinkedHashMap<>();
                            payload.put(binding.action ? "actionId" : "toolId", binding.id);
                            payload.put(binding.action ? "actionName" : "toolName", toolCall.function.name);
                            payload.put("arguments", toolCall.function.arguments);

                            if (!binding.action) {
                                payload.put("toolType", StringUtils.hasText(toolCall.type) ? toolCall.type : "function");
                                payload.put("toolApi", null);
                                payload.put("toolParams", toToolParams(toolCall.function.arguments));
                                payload.put("description", null);
                            } else {
                                payload.put("description", null);
                            }

                            timestampCursor = normalizeEventTimestamp(messageTs, timestampCursor);
                            events.add(event(binding.action ? "action.snapshot" : "tool.snapshot", timestampCursor, seq++, payload));
                            messageTs = timestampCursor + 1;
                        }
                    }
                    continue;
                }

                if (!"tool".equals(role)) {
                    continue;
                }

                String result = textFromContent(message.content);
                if (!StringUtils.hasText(result)) {
                    result = "";
                }

                IdBinding binding = resolveBindingForToolResult(run.runId, message, bindingByCallId, toolIndex, actionIndex);
                if (binding == null) {
                    continue;
                }
                if (binding.action) {
                    actionIndex++;
                } else {
                    toolIndex++;
                }

                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put(binding.action ? "actionId" : "toolId", binding.id);
                payload.put("result", result);
                timestampCursor = normalizeEventTimestamp(messageTs, timestampCursor);
                events.add(event(binding.action ? "action.result" : "tool.result", timestampCursor, seq++, payload));
            }

            timestampCursor = normalizeEventTimestamp(runEndTs + 1, timestampCursor);
            Map<String, Object> runCompletePayload = new LinkedHashMap<>();
            runCompletePayload.put("runId", run.runId);
            runCompletePayload.put("finishReason", "end_turn");
            events.add(event("run.complete", timestampCursor, seq++, runCompletePayload));
        }

        return List.copyOf(events);
    }

    private Map<String, Object> buildRequestQueryPayload(String chatId, RunSnapshot run) {
        Map<String, Object> payload = new LinkedHashMap<>();
        Map<String, Object> query = run.query == null ? Map.of() : run.query;

        Object requestId = query.get("requestId");
        payload.put("requestId", textOrFallback(requestId, run.runId));
        payload.put("chatId", textOrFallback(query.get("chatId"), chatId));
        payload.put("role", textOrFallback(query.get("role"), "user"));
        payload.put("message", textOrFallback(query.get("message"), firstUserText(run.messages)));
        putIfNonNull(payload, "agentKey", query.get("agentKey"));
        putIfNonNull(payload, "references", query.get("references"));
        putIfNonNull(payload, "params", query.get("params"));
        putIfNonNull(payload, "scene", query.get("scene"));
        putIfNonNull(payload, "stream", query.get("stream"));
        return payload;
    }

    private IdBinding resolveBindingForAssistantToolCall(
            String runId,
            ChatWindowMemoryStore.StoredMessage message,
            ChatWindowMemoryStore.StoredToolCall toolCall,
            int toolIndex,
            int actionIndex
    ) {
        // First check outer-level message fields (V3.1)
        if (StringUtils.hasText(message.actionId)) {
            return new IdBinding(message.actionId.trim(), true);
        }
        if (StringUtils.hasText(message.toolId)) {
            return new IdBinding(message.toolId.trim(), false);
        }
        // Fallback to inner toolCall fields (V3 compat)
        boolean actionByType = StringUtils.hasText(toolCall.type)
                && "action".equalsIgnoreCase(toolCall.type.trim());
        if (StringUtils.hasText(toolCall.actionId)) {
            return new IdBinding(toolCall.actionId.trim(), true);
        }
        if (StringUtils.hasText(toolCall.toolId)) {
            return new IdBinding(toolCall.toolId.trim(), false);
        }
        if (actionByType && StringUtils.hasText(toolCall.id)) {
            return new IdBinding(toolCall.id.trim(), true);
        }
        if (StringUtils.hasText(toolCall.id)) {
            return new IdBinding(toolCall.id.trim(), false);
        }
        if (actionByType) {
            return new IdBinding(runId + "_action_" + actionIndex, true);
        }
        return new IdBinding(runId + "_tool_" + toolIndex + "_action_" + actionIndex, false);
    }

    private IdBinding resolveBindingForToolResult(
            String runId,
            ChatWindowMemoryStore.StoredMessage message,
            Map<String, IdBinding> bindingByCallId,
            int toolIndex,
            int actionIndex
    ) {
        if (StringUtils.hasText(message.actionId)) {
            return new IdBinding(message.actionId.trim(), true);
        }
        if (StringUtils.hasText(message.toolId)) {
            return new IdBinding(message.toolId.trim(), false);
        }
        if (StringUtils.hasText(message.toolCallId)) {
            IdBinding binding = bindingByCallId.get(message.toolCallId.trim());
            if (binding != null) {
                return binding;
            }
            return new IdBinding(message.toolCallId.trim(), false);
        }
        if (!StringUtils.hasText(message.name)) {
            return null;
        }
        return new IdBinding(runId + "_tool_result_" + toolIndex + "_action_" + actionIndex, false);
    }

    private Object toToolParams(String arguments) {
        if (!StringUtils.hasText(arguments)) {
            return null;
        }
        try {
            JsonNode parsed = objectMapper.readTree(arguments);
            return objectMapper.convertValue(parsed, Object.class);
        } catch (Exception ex) {
            return null;
        }
    }

    private String firstUserText(List<ChatWindowMemoryStore.StoredMessage> messages) {
        if (messages == null || messages.isEmpty()) {
            return "";
        }
        for (ChatWindowMemoryStore.StoredMessage message : messages) {
            if (message == null || !"user".equalsIgnoreCase(message.role)) {
                continue;
            }
            String text = textFromContent(message.content);
            if (StringUtils.hasText(text)) {
                return text;
            }
        }
        return "";
    }

    private String textFromContent(List<ChatWindowMemoryStore.ContentPart> contentParts) {
        if (contentParts == null || contentParts.isEmpty()) {
            return "";
        }
        StringBuilder text = new StringBuilder();
        for (ChatWindowMemoryStore.ContentPart contentPart : contentParts) {
            if (contentPart == null || !StringUtils.hasText(contentPart.text)) {
                continue;
            }
            text.append(contentPart.text);
        }
        return text.toString();
    }

    private long resolveRunStartTimestamp(RunSnapshot run) {
        long earliest = Long.MAX_VALUE;
        for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
            if (message != null && message.ts != null && message.ts > 0 && message.ts < earliest) {
                earliest = message.ts;
            }
        }
        if (earliest != Long.MAX_VALUE) {
            return earliest;
        }
        if (run.updatedAt > 0) {
            return run.updatedAt;
        }
        return System.currentTimeMillis();
    }

    private long resolveRunEndTimestamp(RunSnapshot run, long fallbackStart) {
        long latest = Long.MIN_VALUE;
        for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
            if (message != null && message.ts != null && message.ts > 0 && message.ts > latest) {
                latest = message.ts;
            }
        }
        if (latest != Long.MIN_VALUE) {
            return latest;
        }
        if (run.updatedAt > 0) {
            return run.updatedAt;
        }
        return fallbackStart;
    }

    private long resolveMessageTimestamp(ChatWindowMemoryStore.StoredMessage message, long fallback) {
        if (message != null && message.ts != null && message.ts > 0) {
            return message.ts;
        }
        return fallback;
    }

    private long normalizeEventTimestamp(long candidate, long previous) {
        if (candidate <= 0) {
            return previous + 1;
        }
        return Math.max(candidate, previous + 1);
    }

    private long sortByUpdatedAt(RunSnapshot run) {
        if (run == null || run.updatedAt <= 0) {
            return Long.MAX_VALUE;
        }
        return run.updatedAt;
    }

    private Map<String, Object> event(String type, long timestamp, long seq, Map<String, Object> payload) {
        Map<String, Object> data = new LinkedHashMap<>();
        data.put("seq", seq);
        data.put("type", type);
        data.put("timestamp", timestamp);
        if (payload != null && !payload.isEmpty()) {
            data.putAll(payload);
        }
        return data;
    }

    private Map<String, Object> planUpdateEvent(
            ChatWindowMemoryStore.PlanSnapshot planSnapshot,
            String chatId,
            long previousTimestamp
    ) {
        if (planSnapshot == null
                || !StringUtils.hasText(planSnapshot.planId)
                || planSnapshot.tasks == null
                || planSnapshot.tasks.isEmpty()) {
            return Map.of();
        }

        List<Map<String, Object>> plan = new ArrayList<>();
        for (ChatWindowMemoryStore.PlanTaskSnapshot task : planSnapshot.tasks) {
            if (task == null || !StringUtils.hasText(task.taskId) || !StringUtils.hasText(task.description)) {
                continue;
            }
            Map<String, Object> item = new LinkedHashMap<>();
            item.put("taskId", task.taskId.trim());
            item.put("description", task.description.trim());
            item.put("status", normalizeStatus(task.status));
            plan.add(item);
        }
        if (plan.isEmpty()) {
            return Map.of();
        }

        Map<String, Object> data = new LinkedHashMap<>();
        data.put("type", "plan.update");
        data.put("planId", planSnapshot.planId.trim());
        data.put("chatId", StringUtils.hasText(chatId) ? chatId : null);
        data.put("plan", plan);
        data.put("rawEvent", null);
        data.put("timestamp", normalizeEventTimestamp(previousTimestamp + 1, previousTimestamp));
        return data;
    }

    private String normalizeStatus(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase();
        return switch (normalized) {
            case "in_progress" -> "init";
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private Map<String, Object> toRawMessageMap(String runId, ChatWindowMemoryStore.StoredMessage message) {
        if (message == null || !StringUtils.hasText(message.role)) {
            return Map.of();
        }
        Map<String, Object> root = objectMapper.convertValue(
                message,
                objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class)
        );
        root.put("runId", runId);
        return root;
    }

    private record StepEntry(
            String stage,
            int seq,
            String taskId,
            long updatedAt,
            ChatWindowMemoryStore.SystemSnapshot system,
            ChatWindowMemoryStore.PlanSnapshot plan,
            List<ChatWindowMemoryStore.StoredMessage> messages,
            int lineIndex
    ) {
    }

    private List<ChatIndexRecord> readIndexRecords() {
        Path path = resolveIndexPath();
        if (!Files.exists(path)) {
            return List.of();
        }
        try {
            List<ChatIndexRecord> records = new ArrayList<>();
            List<String> lines = Files.readAllLines(path, resolveCharset());
            for (String line : lines) {
                if (!StringUtils.hasText(line)) {
                    continue;
                }
                JsonNode node = parseLine(line);
                if (node == null || !node.isObject()) {
                    continue;
                }
                ChatIndexRecord record = objectMapper.treeToValue(node, ChatIndexRecord.class);
                if (record == null || !isValidChatId(record.chatId)) {
                    continue;
                }
                if (!StringUtils.hasText(record.chatName)) {
                    record.chatName = record.chatId;
                }
                if (record.createdAt <= 0 && record.updatedAt > 0) {
                    record.createdAt = record.updatedAt;
                }
                if (record.updatedAt <= 0) {
                    record.updatedAt = record.createdAt;
                }
                records.add(record);
            }
            return List.copyOf(records);
        } catch (Exception ex) {
            log.warn("Cannot read chat index file={}, fallback to empty", path, ex);
            return List.of();
        }
    }

    private void writeIndexRecords(List<ChatIndexRecord> records) {
        Path path = resolveIndexPath();
        try {
            Files.createDirectories(path.getParent());
            StringBuilder content = new StringBuilder();
            for (ChatIndexRecord record : records) {
                if (record == null || !isValidChatId(record.chatId)) {
                    continue;
                }
                if (!StringUtils.hasText(record.chatName)) {
                    record.chatName = record.chatId;
                }
                if (record.createdAt <= 0) {
                    record.createdAt = System.currentTimeMillis();
                }
                if (record.updatedAt <= 0) {
                    record.updatedAt = record.createdAt;
                }
                content.append(objectMapper.writeValueAsString(record)).append(System.lineSeparator());
            }
            Files.writeString(
                    path,
                    content.toString(),
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.TRUNCATE_EXISTING,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot rewrite chat index file=" + path, ex);
        }
    }

    private Charset resolveCharset() {
        String configured = properties.getCharset();
        if (!StringUtils.hasText(configured)) {
            return StandardCharsets.UTF_8;
        }
        try {
            return Charset.forName(configured.trim());
        } catch (Exception ignored) {
            return StandardCharsets.UTF_8;
        }
    }

    private long resolveCreatedAt(Path historyPath) {
        if (historyPath == null || !Files.exists(historyPath)) {
            return System.currentTimeMillis();
        }
        try {
            return Files.getLastModifiedTime(historyPath).toMillis();
        } catch (IOException ex) {
            return System.currentTimeMillis();
        }
    }

    private void requireValidChatId(String chatId) {
        if (!isValidChatId(chatId)) {
            throw new IllegalArgumentException("chatId must be a valid UUID");
        }
    }

    private boolean isValidChatId(String chatId) {
        if (!StringUtils.hasText(chatId)) {
            return false;
        }
        try {
            UUID.fromString(chatId.trim());
            return true;
        } catch (IllegalArgumentException ex) {
            return false;
        }
    }

    private String deriveChatName(String message) {
        String normalized = StringUtils.hasText(message)
                ? message.trim().replaceAll("\\s+", " ")
                : "";
        if (normalized.isEmpty()) {
            return "新对话";
        }
        int[] codePoints = normalized.codePoints().limit(30).toArray();
        return new String(codePoints, 0, codePoints.length);
    }

    private Path resolveBaseDir() {
        return Paths.get(properties.getDir()).toAbsolutePath().normalize();
    }

    private Path resolveIndexPath() {
        return resolveBaseDir().resolve(CHAT_INDEX_FILE);
    }

    private Path resolveHistoryPath(String chatId) {
        return resolveBaseDir().resolve(chatId + ".json");
    }

    private JsonNode parseLine(String line) {
        try {
            return objectMapper.readTree(line);
        } catch (Exception ex) {
            return null;
        }
    }

    private ChatSummary toChatSummary(ChatIndexRecord record) {
        return toChatSummary(record, false);
    }

    private ChatSummary toChatSummary(ChatIndexRecord record, boolean created) {
        long createdAt = record.createdAt > 0 ? record.createdAt : record.updatedAt;
        long updatedAt = record.updatedAt > 0 ? record.updatedAt : createdAt;
        return new ChatSummary(
                record.chatId,
                StringUtils.hasText(record.chatName) ? record.chatName : record.chatId,
                nullable(record.firstAgentKey),
                resolveFirstAgentName(record.firstAgentName, record.firstAgentKey),
                createdAt,
                updatedAt,
                created
        );
    }

    private long resolveUpdatedAt(ChatIndexRecord record) {
        if (record == null) {
            return 0L;
        }
        return record.updatedAt > 0 ? record.updatedAt : record.createdAt;
    }

    private String nullable(String value) {
        return StringUtils.hasText(value) ? value.trim() : null;
    }

    private String resolveFirstAgentName(String firstAgentName, String firstAgentKey) {
        String resolvedName = nullable(firstAgentName);
        if (resolvedName != null) {
            return resolvedName;
        }
        return nullable(firstAgentKey);
    }

    private void putIfNonNull(Map<String, Object> node, String key, Object value) {
        if (value != null) {
            node.put(key, value);
        }
    }

    private String textOrFallback(Object value, String fallback) {
        if (value instanceof String text && StringUtils.hasText(text)) {
            return text.trim();
        }
        return fallback;
    }

    public record ChatSummary(
            String chatId,
            String chatName,
            String firstAgentKey,
            String firstAgentName,
            long createdAt,
            long updatedAt,
            boolean created
    ) {
    }

    private static final class ParsedChatContent {
        private final List<RunSnapshot> runs = new ArrayList<>();
        private final List<Map<String, Object>> rawMessages = new ArrayList<>();
        private final List<Map<String, Object>> events = new ArrayList<>();
        private final LinkedHashMap<String, QueryRequest.Reference> references = new LinkedHashMap<>();
    }

    private record RunSnapshot(
            String runId,
            long updatedAt,
            Map<String, Object> query,
            ChatWindowMemoryStore.SystemSnapshot system,
            ChatWindowMemoryStore.PlanSnapshot plan,
            List<ChatWindowMemoryStore.StoredMessage> messages,
            int lineIndex
    ) {
    }

    private record IdBinding(String id, boolean action) {
    }

    private static final class ChatIndexRecord {
        public String chatId;
        public String chatName;
        public String firstAgentKey;
        public String firstAgentName;
        public long createdAt;
        public long updatedAt;
    }
}
