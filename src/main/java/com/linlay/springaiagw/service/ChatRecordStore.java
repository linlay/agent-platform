package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryProperties;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.agw.AgwChatDetailResponse;
import com.linlay.springaiagw.model.agw.AgwChatSummaryResponse;
import com.linlay.springaiagw.model.agw.AgwQueryRequest;
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
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;

@Service
public class ChatRecordStore {

    private static final Logger log = LoggerFactory.getLogger(ChatRecordStore.class);
    private static final String CHAT_INDEX_FILE = "_chats.jsonl";
    private static final String RECORD_TYPE = "recordType";

    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryProperties properties;
    private final Object lock = new Object();

    public ChatRecordStore(ObjectMapper objectMapper, ChatWindowMemoryProperties properties) {
        this.objectMapper = objectMapper;
        this.properties = properties;
    }

    public ChatSummary ensureChat(String chatId, String agentKey, String firstMessage) {
        requireValidChatId(chatId);
        synchronized (lock) {
            List<ChatIndexRecord> records = readIndexRecords();
            for (ChatIndexRecord record : records) {
                if (chatId.equals(record.chatId)) {
                    return toChatSummary(record);
                }
            }

            long now = System.currentTimeMillis();
            ChatIndexRecord record = new ChatIndexRecord();
            record.chatId = chatId;
            record.chatName = deriveChatName(firstMessage);
            record.firstAgentKey = nullable(agentKey);
            record.createdAt = now;

            appendJsonLine(resolveIndexPath(), objectMapper.valueToTree(record));
            return toChatSummary(record);
        }
    }

    public void appendRequest(
            String chatId,
            String requestId,
            String runId,
            String agentKey,
            String message,
            List<AgwQueryRequest.Reference> references,
            AgwQueryRequest.Scene scene
    ) {
        // recordType snapshots are disabled; request metadata is no longer persisted to chat files.
    }

    public void appendEvent(String chatId, String eventData) {
        // recordType snapshots are disabled; events are only emitted over SSE/console.
    }

    public List<AgwChatSummaryResponse> listChats() {
        synchronized (lock) {
            return readIndexRecords().stream()
                    .sorted((left, right) -> Long.compare(right.createdAt, left.createdAt))
                    .map(record -> new AgwChatSummaryResponse(
                            record.chatId,
                            record.chatName,
                            record.firstAgentKey,
                            record.createdAt
                    ))
                    .toList();
        }
    }

    public AgwChatDetailResponse loadChat(String chatId, boolean includeEvents) {
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
                    .orElseGet(() -> new ChatSummary(chatId, chatId, null, resolveCreatedAt(historyPath)));

            ParsedChatContent content = readChatContent(
                    historyPath,
                    summary.chatId,
                    summary.chatName,
                    includeEvents
            );
            List<Map<String, Object>> events = includeEvents ? List.copyOf(content.events) : null;
            List<AgwQueryRequest.Reference> references = content.references.isEmpty()
                    ? null
                    : List.copyOf(content.references.values());

            return new AgwChatDetailResponse(
                    summary.chatId,
                    summary.chatName,
                    List.copyOf(content.messages),
                    events,
                    references
            );
        }
    }

    private ParsedChatContent readChatContent(
            Path historyPath,
            String chatId,
            String chatName,
            boolean includeEvents
    ) {
        ParsedChatContent content = new ParsedChatContent();
        readHistoryLines(historyPath, content);
        content.runs.sort(
                Comparator.comparingLong(this::sortByUpdatedAt)
                        .thenComparingInt(RunSnapshot::lineIndex)
        );

        for (RunSnapshot run : content.runs) {
            for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
                Map<String, Object> normalized = toMessageMap(run.runId, message);
                if (!normalized.isEmpty()) {
                    content.messages.add(normalized);
                }
            }
        }
        if (includeEvents) {
            content.events.addAll(buildSnapshotEvents(chatId, chatName, content.runs));
        }
        return content;
    }

    private void readHistoryLines(Path historyPath, ParsedChatContent content) {
        if (!Files.exists(historyPath)) {
            return;
        }
        try {
            List<String> lines = Files.readAllLines(historyPath, resolveCharset());
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

                String recordType = textValue(node.get(RECORD_TYPE));
                if (StringUtils.hasText(recordType)) {
                    collectReferences(node, content);
                    lineIndex++;
                    continue;
                }

                ChatWindowMemoryStore.RunRecord runRecord = toRunRecord(node, lineIndex);
                if (runRecord == null || runRecord.messages == null || runRecord.messages.isEmpty()) {
                    lineIndex++;
                    continue;
                }
                content.runs.add(new RunSnapshot(
                        runRecord.runId,
                        runRecord.updatedAt,
                        List.copyOf(runRecord.messages),
                        lineIndex
                ));
                lineIndex++;
            }
        } catch (Exception ex) {
            log.warn("Cannot read chat history file={}, fallback to empty", historyPath, ex);
        }
    }

    private void collectReferences(JsonNode node, ParsedChatContent content) {
        JsonNode referencesNode = node.get("references");
        if ((referencesNode == null || referencesNode.isNull()) && node.has("payload")) {
            referencesNode = node.path("payload").get("references");
        }
        if (referencesNode == null || !referencesNode.isArray()) {
            return;
        }

        for (JsonNode referenceNode : referencesNode) {
            if (referenceNode == null || !referenceNode.isObject()) {
                continue;
            }
            try {
                AgwQueryRequest.Reference reference = objectMapper.treeToValue(referenceNode, AgwQueryRequest.Reference.class);
                if (reference == null || !StringUtils.hasText(reference.id())) {
                    continue;
                }
                content.references.putIfAbsent(reference.id().trim(), reference);
            } catch (Exception ignored) {
                // Ignore invalid reference entry and continue parsing the rest.
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
        for (RunSnapshot run : runs) {
            if (run.messages == null || run.messages.isEmpty()) {
                continue;
            }

            long runStartTs = resolveRunStartTimestamp(run);
            long runEndTs = resolveRunEndTimestamp(run, runStartTs);
            String firstUserMessage = firstUserText(run.messages);
            String contentId = run.runId + "_0";
            int eventIndex = 1;
            Set<String> emittedToolCallIds = new HashSet<>();

            Map<String, Object> query = event("query.message", runStartTs);
            query.put("requestId", run.runId);
            query.put("chatId", chatId);
            query.put("message", firstUserMessage);
            events.add(query);

            Map<String, Object> chatStart = event("chat.start", runStartTs + 1);
            chatStart.put("chatId", chatId);
            if (StringUtils.hasText(chatName)) {
                chatStart.put("chatName", chatName);
            }
            events.add(chatStart);

            Map<String, Object> runStart = event("run.start", runStartTs + 2);
            runStart.put("runId", run.runId);
            runStart.put("chatId", chatId);
            events.add(runStart);

            for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
                if (isAssistantTextMessage(message)) {
                    Map<String, Object> messageSnapshot = event("message.snapshot", resolveMessageTimestamp(message, runStartTs));
                    messageSnapshot.put("messageId", run.runId + "_" + eventIndex++);
                    messageSnapshot.put("contentId", contentId);
                    messageSnapshot.put("role", "assistant");
                    messageSnapshot.put("text", message.content);
                    events.add(messageSnapshot);
                }

                if (isAssistantToolCallMessage(message)) {
                    Map<String, Object> toolSnapshot = toToolSnapshot(run.runId, contentId, eventIndex++, message, runStartTs);
                    events.add(toolSnapshot);
                    if (StringUtils.hasText(message.toolCallId)) {
                        emittedToolCallIds.add(message.toolCallId.trim());
                    }
                    continue;
                }

                if (isToolMessage(message) && shouldEmitToolSnapshot(message, emittedToolCallIds)) {
                    Map<String, Object> toolSnapshot = toToolSnapshot(run.runId, contentId, eventIndex++, message, runStartTs);
                    events.add(toolSnapshot);
                    if (StringUtils.hasText(message.toolCallId)) {
                        emittedToolCallIds.add(message.toolCallId.trim());
                    }
                }
            }

            Map<String, Object> runComplete = event("run.complete", runEndTs + 1);
            runComplete.put("runId", run.runId);
            events.add(runComplete);
        }
        return List.copyOf(events);
    }

    private Map<String, Object> toToolSnapshot(
            String runId,
            String contentId,
            int eventIndex,
            ChatWindowMemoryStore.StoredMessage message,
            long fallbackTimestamp
    ) {
        Map<String, Object> snapshot = event("tool.snapshot", resolveMessageTimestamp(message, fallbackTimestamp));
        snapshot.put("toolId", runId + "_" + eventIndex);
        snapshot.put("toolName", StringUtils.hasText(message.name) ? message.name.trim() : "unknown_tool");
        snapshot.put("contentId", contentId);
        snapshot.put("toolType", null);
        snapshot.put("toolApi", null);
        snapshot.put("toolParams", toToolParams(message.toolArgs));
        snapshot.put("description", null);
        snapshot.put("arguments", stringifyToolArguments(message.toolArgs));
        return snapshot;
    }

    private Object toToolParams(JsonNode toolArgs) {
        if (toolArgs == null || toolArgs.isNull()) {
            return null;
        }
        return objectMapper.convertValue(toolArgs, Object.class);
    }

    private String stringifyToolArguments(JsonNode toolArgs) {
        if (toolArgs == null || toolArgs.isNull()) {
            return null;
        }
        if (toolArgs.isTextual()) {
            return toolArgs.asText();
        }
        try {
            return objectMapper.writeValueAsString(toolArgs);
        } catch (Exception ex) {
            return String.valueOf(toolArgs);
        }
    }

    private boolean isAssistantTextMessage(ChatWindowMemoryStore.StoredMessage message) {
        return message != null
                && "assistant".equalsIgnoreCase(message.role)
                && StringUtils.hasText(message.content);
    }

    private boolean isAssistantToolCallMessage(ChatWindowMemoryStore.StoredMessage message) {
        return message != null
                && "assistant".equalsIgnoreCase(message.role)
                && StringUtils.hasText(message.name)
                && StringUtils.hasText(message.toolCallId);
    }

    private boolean isToolMessage(ChatWindowMemoryStore.StoredMessage message) {
        return message != null
                && "tool".equalsIgnoreCase(message.role)
                && StringUtils.hasText(message.name);
    }

    private boolean shouldEmitToolSnapshot(ChatWindowMemoryStore.StoredMessage message, Set<String> emittedToolCallIds) {
        String toolCallId = nullable(message.toolCallId);
        if (toolCallId == null) {
            return true;
        }
        return !emittedToolCallIds.contains(toolCallId);
    }

    private String firstUserText(List<ChatWindowMemoryStore.StoredMessage> messages) {
        for (ChatWindowMemoryStore.StoredMessage message : messages) {
            if (message == null || !"user".equalsIgnoreCase(message.role)) {
                continue;
            }
            if (StringUtils.hasText(message.content)) {
                return message.content;
            }
        }
        return "";
    }

    private long resolveRunStartTimestamp(RunSnapshot run) {
        long earliest = Long.MAX_VALUE;
        for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
            if (message != null && message.ts > 0 && message.ts < earliest) {
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
            if (message != null && message.ts > 0 && message.ts > latest) {
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
        if (message != null && message.ts > 0) {
            return message.ts;
        }
        return fallback;
    }

    private long sortByUpdatedAt(RunSnapshot run) {
        if (run == null || run.updatedAt <= 0) {
            return Long.MAX_VALUE;
        }
        return run.updatedAt;
    }

    private Map<String, Object> event(String type, long timestamp) {
        Map<String, Object> data = new LinkedHashMap<>();
        data.put("type", type);
        data.put("timestamp", timestamp);
        return data;
    }

    private Map<String, Object> toMessageMap(String runId, ChatWindowMemoryStore.StoredMessage message) {
        if (message == null || !StringUtils.hasText(message.role)) {
            return Map.of();
        }
        Map<String, Object> root = new LinkedHashMap<>();
        root.put("role", message.role);
        root.put("content", message.content);
        root.put("ts", message.ts);
        putIfText(root, "runId", runId);
        putIfText(root, "name", message.name);
        putIfText(root, "toolCallId", message.toolCallId);
        putIfText(root, "toolResult", message.toolResult);
        if (message.toolArgs != null && !message.toolArgs.isNull()) {
            root.put("toolArgs", objectMapper.convertValue(message.toolArgs, Object.class));
        }
        return root;
    }

    private ChatWindowMemoryStore.RunRecord toRunRecord(JsonNode node, int lineIndex) {
        JsonNode messages = node.get("messages");
        if (messages == null || !messages.isArray()) {
            return null;
        }
        try {
            ChatWindowMemoryStore.RunRecord run = objectMapper.treeToValue(node, ChatWindowMemoryStore.RunRecord.class);
            if (run == null) {
                return null;
            }
            if (!StringUtils.hasText(run.runId)) {
                run.runId = "legacy-" + lineIndex;
            }
            return run;
        } catch (Exception ex) {
            return null;
        }
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
                records.add(record);
            }
            return List.copyOf(records);
        } catch (Exception ex) {
            log.warn("Cannot read chat index file={}, fallback to empty", path, ex);
            return List.of();
        }
    }

    private void appendJsonLine(Path path, JsonNode node) {
        try {
            Files.createDirectories(path.getParent());
            String line = objectMapper.writeValueAsString(node) + System.lineSeparator();
            Files.writeString(
                    path,
                    line,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.APPEND,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot append chat record file=" + path, ex);
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
        int[] codePoints = normalized.codePoints().limit(10).toArray();
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
        return new ChatSummary(
                record.chatId,
                StringUtils.hasText(record.chatName) ? record.chatName : record.chatId,
                nullable(record.firstAgentKey),
                record.createdAt
        );
    }

    private String nullable(String value) {
        return StringUtils.hasText(value) ? value.trim() : null;
    }

    private void putIfText(Map<String, Object> node, String key, String value) {
        if (StringUtils.hasText(value)) {
            node.put(key, value.trim());
        }
    }

    private String textValue(JsonNode node) {
        if (node == null || node.isNull()) {
            return null;
        }
        String text = node.asText();
        return StringUtils.hasText(text) ? text.trim() : null;
    }

    public record ChatSummary(
            String chatId,
            String chatName,
            String firstAgentKey,
            long createdAt
    ) {
    }

    private static final class ParsedChatContent {
        private final List<RunSnapshot> runs = new ArrayList<>();
        private final List<Map<String, Object>> messages = new ArrayList<>();
        private final List<Map<String, Object>> events = new ArrayList<>();
        private final LinkedHashMap<String, AgwQueryRequest.Reference> references = new LinkedHashMap<>();
    }

    private record RunSnapshot(
            String runId,
            long updatedAt,
            List<ChatWindowMemoryStore.StoredMessage> messages,
            int lineIndex
    ) {
    }

    private static final class ChatIndexRecord {
        public String chatId;
        public String chatName;
        public String firstAgentKey;
        public long createdAt;
    }
}
