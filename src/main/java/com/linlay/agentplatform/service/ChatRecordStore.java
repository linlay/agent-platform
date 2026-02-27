package com.linlay.agentplatform.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.memory.ChatWindowMemoryProperties;
import com.linlay.agentplatform.memory.ChatWindowMemoryStore;
import com.linlay.agentplatform.model.api.AgentChatSummaryResponse;
import com.linlay.agentplatform.model.api.ChatDetailResponse;
import com.linlay.agentplatform.model.api.ChatSummaryResponse;
import com.linlay.agentplatform.model.api.QueryRequest;
import jakarta.annotation.PostConstruct;
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
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
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
    private static final String CHAT_INDEX_FILE_LEGACY = "_chats.jsonl";
    private static final String EVENT_KIND_RUN_COMPLETED = "RUN_COMPLETED";
    private static final String EVENT_STATE_PENDING = "PENDING";
    private static final String EVENT_STATE_ACKED = "ACKED";

    private static final Set<String> ALLOWED_AGENT_CHAT_SORTS = Set.of(
            "LATEST_CHAT_TIME_DESC",
            "UNREAD_CHAT_COUNT_DESC",
            "AGENT_NAME_ASC"
    );

    private static final String CREATE_CHAT_INDEX_SQL = """
            CREATE TABLE IF NOT EXISTS CHAT_INDEX_ (
              CHAT_ID_ TEXT PRIMARY KEY,
              CHAT_NAME_ TEXT NOT NULL,
              AGENT_KEY_ TEXT NOT NULL,
              AGENT_NAME_ TEXT NOT NULL,
              AGENT_AVATAR_ TEXT,
              CREATED_AT_ INTEGER NOT NULL,
              UPDATED_AT_ INTEGER NOT NULL,
              LAST_CHAT_CONTENT_ TEXT NOT NULL DEFAULT '',
              LAST_CHAT_TIME_ INTEGER NOT NULL,
              LAST_RUN_ID_ TEXT
            )
            """;
    private static final String CREATE_CHAT_INDEX_UPDATED_INDEX_SQL = """
            CREATE INDEX IF NOT EXISTS IDX_CHAT_INDEX_UPDATED_AT_
              ON CHAT_INDEX_(UPDATED_AT_ DESC)
            """;
    private static final String CREATE_CHAT_INDEX_AGENT_UPDATED_INDEX_SQL = """
            CREATE INDEX IF NOT EXISTS IDX_CHAT_INDEX_AGENT_KEY_UPDATED_AT_
              ON CHAT_INDEX_(AGENT_KEY_, UPDATED_AT_ DESC)
            """;
    private static final String CREATE_NOTIFY_QUEUE_SQL = """
            CREATE TABLE IF NOT EXISTS CHAT_NOTIFY_QUEUE_ (
              EVENT_ID_ INTEGER PRIMARY KEY AUTOINCREMENT,
              AGENT_KEY_ TEXT NOT NULL,
              CHAT_ID_ TEXT NOT NULL,
              RUN_ID_ TEXT NOT NULL,
              EVENT_KIND_ TEXT NOT NULL,
              STATE_ TEXT NOT NULL,
              ENQUEUE_AT_ INTEGER NOT NULL,
              ACK_AT_ INTEGER,
              PAYLOAD_JSON_ TEXT
            )
            """;
    private static final String CREATE_NOTIFY_QUEUE_UQ_SQL = """
            CREATE UNIQUE INDEX IF NOT EXISTS UQ_CHAT_NOTIFY_QUEUE_RUN_KIND_
              ON CHAT_NOTIFY_QUEUE_(RUN_ID_, EVENT_KIND_)
            """;
    private static final String CREATE_NOTIFY_QUEUE_AGENT_STATE_INDEX_SQL = """
            CREATE INDEX IF NOT EXISTS IDX_CHAT_NOTIFY_QUEUE_AGENT_STATE_EVENT_
              ON CHAT_NOTIFY_QUEUE_(AGENT_KEY_, STATE_, EVENT_ID_)
            """;
    private static final String CREATE_AGENT_DIALOG_INDEX_SQL = """
            CREATE TABLE IF NOT EXISTS AGENT_DIALOG_INDEX_ (
              AGENT_KEY_ TEXT PRIMARY KEY,
              AGENT_NAME_ TEXT NOT NULL,
              AGENT_AVATAR_ TEXT,
              LATEST_CHAT_ID_ TEXT,
              LATEST_CHAT_NAME_ TEXT,
              LATEST_CHAT_CONTENT_ TEXT NOT NULL DEFAULT '',
              LATEST_CHAT_TIME_ INTEGER NOT NULL DEFAULT 0,
              UNREAD_CHAT_COUNT_ INTEGER NOT NULL DEFAULT 0,
              UPDATED_AT_ INTEGER NOT NULL
            )
            """;
    private static final String CREATE_AGENT_DIALOG_LATEST_INDEX_SQL = """
            CREATE INDEX IF NOT EXISTS IDX_AGENT_DIALOG_INDEX_LATEST_CHAT_TIME_
              ON AGENT_DIALOG_INDEX_(LATEST_CHAT_TIME_ DESC)
            """;

    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryProperties properties;
    private final Object lock = new Object();

    public ChatRecordStore(ObjectMapper objectMapper, ChatWindowMemoryProperties properties) {
        this.objectMapper = objectMapper;
        this.properties = properties;
    }

    @PostConstruct
    public void initializeDatabase() {
        synchronized (lock) {
            Path dbPath = resolveSqlitePath();
            Path parent = dbPath.getParent();
            try {
                if (parent != null) {
                    Files.createDirectories(parent);
                }
            } catch (IOException ex) {
                throw new IllegalStateException("Cannot create sqlite directory for " + dbPath, ex);
            }

            Path legacyIndex = resolveBaseDir().resolve(CHAT_INDEX_FILE_LEGACY);
            if (Files.exists(legacyIndex)) {
                log.warn("Legacy chat index detected and ignored: {}", legacyIndex.toAbsolutePath().normalize());
            }

            try (Connection connection = openConnection();
                 Statement statement = connection.createStatement()) {
                statement.execute(CREATE_CHAT_INDEX_SQL);
                statement.execute(CREATE_CHAT_INDEX_UPDATED_INDEX_SQL);
                statement.execute(CREATE_CHAT_INDEX_AGENT_UPDATED_INDEX_SQL);
                statement.execute(CREATE_NOTIFY_QUEUE_SQL);
                statement.execute(CREATE_NOTIFY_QUEUE_UQ_SQL);
                statement.execute(CREATE_NOTIFY_QUEUE_AGENT_STATE_INDEX_SQL);
                statement.execute(CREATE_AGENT_DIALOG_INDEX_SQL);
                statement.execute(CREATE_AGENT_DIALOG_LATEST_INDEX_SQL);
                ensureColumnExists(connection, "CHAT_INDEX_", "AGENT_AVATAR_", "TEXT");
                ensureColumnExists(connection, "CHAT_INDEX_", "LAST_CHAT_CONTENT_", "TEXT NOT NULL DEFAULT ''");
                ensureColumnExists(connection, "CHAT_INDEX_", "LAST_CHAT_TIME_", "INTEGER NOT NULL DEFAULT 0");
                ensureColumnExists(connection, "CHAT_INDEX_", "LAST_RUN_ID_", "TEXT");
                ensureColumnExists(connection, "AGENT_DIALOG_INDEX_", "AGENT_AVATAR_", "TEXT");
                ensureColumnExists(connection, "AGENT_DIALOG_INDEX_", "LATEST_CHAT_CONTENT_", "TEXT NOT NULL DEFAULT ''");
                ensureColumnExists(connection, "AGENT_DIALOG_INDEX_", "LATEST_CHAT_TIME_", "INTEGER NOT NULL DEFAULT 0");
                ensureColumnExists(connection, "AGENT_DIALOG_INDEX_", "UNREAD_CHAT_COUNT_", "INTEGER NOT NULL DEFAULT 0");
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot initialize sqlite chat index", ex);
            }
        }
    }

    public ChatSummary ensureChat(String chatId, String firstAgentKey, String firstAgentName, String firstMessage) {
        requireValidChatId(chatId);
        String normalizedAgentKey = nullable(firstAgentKey);
        if (!StringUtils.hasText(normalizedAgentKey)) {
            throw new IllegalArgumentException("agentKey must not be blank");
        }
        String normalizedAgentName = resolveFirstAgentName(firstAgentName, normalizedAgentKey);
        String normalizedChatName = deriveChatName(firstMessage);
        long now = System.currentTimeMillis();
        synchronized (lock) {
            try (Connection connection = openConnection()) {
                connection.setAutoCommit(false);
                ChatIndexRecord existing = findChatRecordById(connection, chatId);
                boolean created = existing == null;
                ChatIndexRecord record = created ? new ChatIndexRecord() : existing;
                if (created) {
                    record.chatId = chatId;
                    record.chatName = normalizedChatName;
                    record.firstAgentKey = normalizedAgentKey;
                    record.firstAgentName = normalizedAgentName;
                    record.agentAvatar = null;
                    record.createdAt = now;
                    record.updatedAt = now;
                    record.lastChatContent = StringUtils.hasText(firstMessage) ? firstMessage.trim() : "";
                    record.lastChatTime = now;
                    record.lastRunId = null;
                } else {
                    if (!StringUtils.hasText(record.chatName)) {
                        record.chatName = normalizedChatName;
                    }
                    if (!StringUtils.hasText(record.firstAgentName)) {
                        record.firstAgentName = resolveFirstAgentName(normalizedAgentName, record.firstAgentKey);
                    }
                    if (record.createdAt <= 0) {
                        record.createdAt = now;
                    }
                    record.updatedAt = now;
                    if (record.lastChatTime <= 0) {
                        record.lastChatTime = now;
                    }
                    if (!StringUtils.hasText(record.lastChatContent) && StringUtils.hasText(firstMessage)) {
                        record.lastChatContent = firstMessage.trim();
                    }
                }
                upsertChatIndex(connection, record);
                refreshAgentDialogIndex(connection, record.firstAgentKey);
                connection.commit();
                return toChatSummary(record, created);
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot upsert chat index for chatId=" + chatId, ex);
            }
        }
    }

    public void appendEvent(String chatId, String eventData) {
        if (!isValidChatId(chatId) || !StringUtils.hasText(eventData)) {
            return;
        }
        JsonNode node = parseLine(eventData);
        if (node == null || !node.isObject()) {
            return;
        }

        String type = textValue(node.get("type"));
        if (!isPersistedEventType(type)) {
            return;
        }
        String runId = textValue(node.get("runId"));
        if (!StringUtils.hasText(runId)) {
            return;
        }

        long timestamp = node.path("timestamp").asLong(System.currentTimeMillis());
        Map<String, Object> event = objectMapper.convertValue(
                node,
                objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class)
        );

        Map<String, Object> line = new LinkedHashMap<>();
        line.put("_type", "event");
        line.put("chatId", chatId);
        line.put("runId", runId);
        line.put("updatedAt", timestamp > 0 ? timestamp : System.currentTimeMillis());
        line.put("event", event);

        synchronized (lock) {
            appendJsonLine(resolveHistoryPath(chatId), line);
        }
    }

    public Optional<String> findBoundAgentKey(String chatId) {
        if (!isValidChatId(chatId)) {
            return Optional.empty();
        }
        synchronized (lock) {
            try (Connection connection = openConnection()) {
                ChatIndexRecord record = findChatRecordById(connection, chatId);
                if (record == null || !StringUtils.hasText(record.firstAgentKey)) {
                    return Optional.empty();
                }
                return Optional.of(record.firstAgentKey);
            } catch (SQLException ex) {
                log.warn("Cannot query bound agent for chatId={}", chatId, ex);
                return Optional.empty();
            }
        }
    }

    public void onRunCompleted(RunCompletion completion) {
        if (completion == null || !isValidChatId(completion.chatId()) || !StringUtils.hasText(completion.runId())) {
            return;
        }
        synchronized (lock) {
            try (Connection connection = openConnection()) {
                connection.setAutoCommit(false);
                ChatIndexRecord record = findChatRecordById(connection, completion.chatId());
                if (record == null || !StringUtils.hasText(record.firstAgentKey)) {
                    connection.rollback();
                    return;
                }

                long eventAt = completion.completedAt() > 0 ? completion.completedAt() : System.currentTimeMillis();
                String assistantContent = nullable(completion.assistantContent());
                String fallbackUserMessage = nullable(completion.fallbackUserMessage());
                String mergedContent = assistantContent != null
                        ? assistantContent
                        : (fallbackUserMessage != null ? fallbackUserMessage : nullable(record.lastChatContent));
                if (mergedContent == null) {
                    mergedContent = "";
                }

                record.lastRunId = completion.runId().trim();
                record.lastChatTime = eventAt;
                record.updatedAt = eventAt;
                record.lastChatContent = mergedContent;
                upsertChatIndex(connection, record);

                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("chatId", record.chatId);
                payload.put("runId", record.lastRunId);
                payload.put("latestChatTime", record.lastChatTime);
                payload.put("latestChatContent", record.lastChatContent);
                upsertNotifyQueue(
                        connection,
                        record.firstAgentKey,
                        record.chatId,
                        record.lastRunId,
                        eventAt,
                        objectMapper.writeValueAsString(payload)
                );
                refreshAgentDialogIndex(connection, record.firstAgentKey);
                connection.commit();
            } catch (Exception ex) {
                log.warn("Cannot update run completion index chatId={}, runId={}", completion.chatId(), completion.runId(), ex);
            }
        }
    }

    public List<ChatSummaryResponse> listChats() {
        synchronized (lock) {
            try (Connection connection = openConnection();
                 PreparedStatement statement = connection.prepareStatement("""
                         SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_,
                                CREATED_AT_, UPDATED_AT_, LAST_CHAT_CONTENT_, LAST_CHAT_TIME_, LAST_RUN_ID_
                         FROM CHAT_INDEX_
                         ORDER BY UPDATED_AT_ DESC
                         """);
                 ResultSet resultSet = statement.executeQuery()) {
                List<ChatSummaryResponse> responses = new ArrayList<>();
                while (resultSet.next()) {
                    ChatIndexRecord record = mapChatIndexRecord(resultSet);
                    ChatSummary summary = toChatSummary(record);
                    responses.add(new ChatSummaryResponse(
                            summary.chatId(),
                            summary.chatName(),
                            summary.firstAgentKey(),
                            summary.firstAgentName(),
                            summary.createdAt(),
                            summary.updatedAt(),
                            summary.agentAvatar()
                    ));
                }
                return List.copyOf(responses);
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot list chats from sqlite", ex);
            }
        }
    }

    public List<AgentChatSummaryResponse> listAgentChats(String rawSort) {
        String normalizedSort = normalizeAgentChatSort(rawSort);
        String orderClause = switch (normalizedSort) {
            case "UNREAD_CHAT_COUNT_DESC" -> "UNREAD_CHAT_COUNT_ DESC, LATEST_CHAT_TIME_ DESC";
            case "AGENT_NAME_ASC" -> "AGENT_NAME_ ASC, LATEST_CHAT_TIME_ DESC";
            default -> "LATEST_CHAT_TIME_ DESC, UPDATED_AT_ DESC";
        };
        String sql = """
                SELECT AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_, LATEST_CHAT_ID_, LATEST_CHAT_NAME_,
                       LATEST_CHAT_CONTENT_, LATEST_CHAT_TIME_, UNREAD_CHAT_COUNT_
                FROM AGENT_DIALOG_INDEX_
                ORDER BY %s
                """.formatted(orderClause);
        synchronized (lock) {
            try (Connection connection = openConnection();
                 PreparedStatement statement = connection.prepareStatement(sql);
                 ResultSet resultSet = statement.executeQuery()) {
                List<AgentChatSummaryResponse> responses = new ArrayList<>();
                while (resultSet.next()) {
                    responses.add(new AgentChatSummaryResponse(
                            nullable(resultSet.getString("AGENT_KEY_")),
                            nullable(resultSet.getString("AGENT_NAME_")),
                            nullable(resultSet.getString("AGENT_AVATAR_")),
                            nullable(resultSet.getString("LATEST_CHAT_ID_")),
                            nullable(resultSet.getString("LATEST_CHAT_NAME_")),
                            nullable(resultSet.getString("LATEST_CHAT_CONTENT_")),
                            resultSet.getLong("LATEST_CHAT_TIME_"),
                            resultSet.getLong("UNREAD_CHAT_COUNT_")
                    ));
                }
                return List.copyOf(responses);
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot list agent chats from sqlite", ex);
            }
        }
    }

    public MarkReadResult markAgentRead(String agentKey) {
        String normalizedAgentKey = nullable(agentKey);
        if (!StringUtils.hasText(normalizedAgentKey)) {
            throw new IllegalArgumentException("agentKey must not be blank");
        }
        synchronized (lock) {
            try (Connection connection = openConnection()) {
                connection.setAutoCommit(false);
                long ackedChats = countDistinctPendingChats(connection, normalizedAgentKey);
                long ackedEvents = markAllPendingAsAcked(connection, normalizedAgentKey, System.currentTimeMillis());
                refreshAgentDialogIndex(connection, normalizedAgentKey);
                long unreadAfter = countDistinctPendingChats(connection, normalizedAgentKey);
                connection.commit();
                return new MarkReadResult(normalizedAgentKey, ackedEvents, ackedChats, unreadAfter);
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot ack agent read for " + normalizedAgentKey, ex);
            }
        }
    }

    public ChatDetailResponse loadChat(String chatId, boolean includeRawMessages) {
        requireValidChatId(chatId);
        Path historyPath = resolveHistoryPath(chatId);
        synchronized (lock) {
            ChatIndexRecord indexRecord;
            try (Connection connection = openConnection()) {
                indexRecord = findChatRecordById(connection, chatId);
            } catch (SQLException ex) {
                throw new IllegalStateException("Cannot query chat index for chatId=" + chatId, ex);
            }

            if (indexRecord == null && !Files.exists(historyPath)) {
                throw new ChatNotFoundException(chatId);
            }

            ChatSummary summary = Optional.ofNullable(indexRecord)
                    .map(this::toChatSummary)
                    .orElseGet(() -> {
                        long createdAt = resolveCreatedAt(historyPath);
                        return new ChatSummary(chatId, chatId, null, null, createdAt, createdAt, null, false);
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
            LinkedHashMap<String, List<PersistedEvent>> eventsByRunId = new LinkedHashMap<>();
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
                    eventsByRunId.computeIfAbsent(runId, k -> new ArrayList<>());
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
                    eventsByRunId.computeIfAbsent(runId, k -> new ArrayList<>());
                } else if ("event".equals(type)) {
                    JsonNode eventNode = node.has("event") && node.get("event").isObject()
                            ? node.get("event")
                            : node;
                    String eventType = textValue(eventNode.get("type"));
                    if (!isPersistedEventType(eventType)) {
                        lineIndex++;
                        continue;
                    }
                    long eventTs = eventNode.path("timestamp").asLong(node.path("updatedAt").asLong(0));
                    Map<String, Object> eventPayload = objectMapper.convertValue(
                            eventNode,
                            objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class)
                    );
                    eventPayload.remove("seq");
                    eventPayload.remove("type");
                    eventPayload.remove("timestamp");
                    if (!eventPayload.containsKey("chatId")) {
                        String eventChatId = textValue(node.get("chatId"));
                        if (StringUtils.hasText(eventChatId)) {
                            eventPayload.put("chatId", eventChatId);
                        }
                    }
                    stepsByRunId.computeIfAbsent(runId, k -> new ArrayList<>());
                    eventsByRunId.computeIfAbsent(runId, k -> new ArrayList<>())
                            .add(new PersistedEvent(eventType, eventTs, eventPayload, lineIndex));
                }
                lineIndex++;
            }

            // Build RunSnapshots from grouped data
            int runIndex = 0;
            for (Map.Entry<String, List<StepEntry>> entry : stepsByRunId.entrySet()) {
                String runId = entry.getKey();
                List<StepEntry> steps = entry.getValue();
                List<PersistedEvent> persistedEvents = eventsByRunId.getOrDefault(runId, List.of());
                if (steps.isEmpty() && persistedEvents.isEmpty()) {
                    runIndex++;
                    continue;
                }

                if (!steps.isEmpty()) {
                    steps.sort(Comparator.comparingInt(s -> s.seq));
                }

                Map<String, Object> query = queryByRunId.getOrDefault(runId, Map.of());
                long updatedAt = Math.max(
                        steps.stream().mapToLong(s -> s.updatedAt).max().orElse(0),
                        persistedEvents.stream().mapToLong(PersistedEvent::timestamp).max().orElse(0)
                );

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

                int firstLineIndex = !steps.isEmpty()
                        ? steps.getFirst().lineIndex
                        : persistedEvents.stream().mapToInt(PersistedEvent::lineIndex).min().orElse(lineIndex);
                content.runs.add(new RunSnapshot(
                        runId,
                        updatedAt,
                        query,
                        firstSystem,
                        latestPlan,
                        List.copyOf(allMessages),
                        List.copyOf(persistedEvents),
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
            List<PersistedEvent> persistedEvents = run.persistedEvents.stream()
                    .sorted(Comparator.comparingLong(PersistedEvent::timestamp)
                            .thenComparingInt(PersistedEvent::lineIndex))
                    .toList();
            int persistedIndex = 0;

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

            while (persistedIndex < persistedEvents.size()
                    && persistedEvents.get(persistedIndex).timestamp() <= timestampCursor) {
                PersistedEvent persisted = persistedEvents.get(persistedIndex++);
                long persistedTs = normalizeEventTimestamp(persisted.timestamp(), timestampCursor);
                events.add(event(persisted.type(), persistedTs, seq++, persisted.payload()));
                timestampCursor = persistedTs;
            }

            for (ChatWindowMemoryStore.StoredMessage message : run.messages) {
                if (message == null || !StringUtils.hasText(message.role)) {
                    continue;
                }
                long messageTs = normalizeEventTimestamp(resolveMessageTimestamp(message, timestampCursor), timestampCursor);
                while (persistedIndex < persistedEvents.size()
                        && persistedEvents.get(persistedIndex).timestamp() <= messageTs) {
                    PersistedEvent persisted = persistedEvents.get(persistedIndex++);
                    long persistedTs = normalizeEventTimestamp(persisted.timestamp(), timestampCursor);
                    events.add(event(persisted.type(), persistedTs, seq++, persisted.payload()));
                    timestampCursor = persistedTs;
                }
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

            while (persistedIndex < persistedEvents.size()) {
                PersistedEvent persisted = persistedEvents.get(persistedIndex++);
                long persistedTs = normalizeEventTimestamp(persisted.timestamp(), timestampCursor);
                events.add(event(persisted.type(), persistedTs, seq++, persisted.payload()));
                timestampCursor = persistedTs;
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

    private Connection openConnection() throws SQLException {
        return DriverManager.getConnection("jdbc:sqlite:" + resolveSqlitePath());
    }

    private Path resolveSqlitePath() {
        ChatWindowMemoryProperties.IndexProperties index = properties.getIndex();
        String configured = index == null ? null : index.getSqliteFile();
        if (!StringUtils.hasText(configured)) {
            configured = "chats.db";
        }
        Path path = Paths.get(configured.trim());
        if (!path.isAbsolute()) {
            path = Paths.get(System.getProperty("user.dir")).resolve(path).normalize();
        }
        return path.toAbsolutePath().normalize();
    }

    private ChatIndexRecord findChatRecordById(Connection connection, String chatId) throws SQLException {
        try (PreparedStatement statement = connection.prepareStatement("""
                SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_,
                       CREATED_AT_, UPDATED_AT_, LAST_CHAT_CONTENT_, LAST_CHAT_TIME_, LAST_RUN_ID_
                FROM CHAT_INDEX_
                WHERE CHAT_ID_ = ?
                """)) {
            statement.setString(1, chatId);
            try (ResultSet resultSet = statement.executeQuery()) {
                if (!resultSet.next()) {
                    return null;
                }
                return mapChatIndexRecord(resultSet);
            }
        }
    }

    private ChatIndexRecord mapChatIndexRecord(ResultSet resultSet) throws SQLException {
        ChatIndexRecord record = new ChatIndexRecord();
        record.chatId = nullable(resultSet.getString("CHAT_ID_"));
        record.chatName = nullable(resultSet.getString("CHAT_NAME_"));
        record.firstAgentKey = nullable(resultSet.getString("AGENT_KEY_"));
        record.firstAgentName = nullable(resultSet.getString("AGENT_NAME_"));
        record.agentAvatar = nullable(resultSet.getString("AGENT_AVATAR_"));
        record.createdAt = resultSet.getLong("CREATED_AT_");
        record.updatedAt = resultSet.getLong("UPDATED_AT_");
        record.lastChatContent = nullable(resultSet.getString("LAST_CHAT_CONTENT_"));
        record.lastChatTime = resultSet.getLong("LAST_CHAT_TIME_");
        record.lastRunId = nullable(resultSet.getString("LAST_RUN_ID_"));
        return record;
    }

    private void upsertChatIndex(Connection connection, ChatIndexRecord record) throws SQLException {
        try (PreparedStatement statement = connection.prepareStatement("""
                INSERT INTO CHAT_INDEX_(
                    CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_,
                    CREATED_AT_, UPDATED_AT_, LAST_CHAT_CONTENT_, LAST_CHAT_TIME_, LAST_RUN_ID_
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(CHAT_ID_) DO UPDATE SET
                    CHAT_NAME_ = excluded.CHAT_NAME_,
                    AGENT_KEY_ = excluded.AGENT_KEY_,
                    AGENT_NAME_ = excluded.AGENT_NAME_,
                    AGENT_AVATAR_ = excluded.AGENT_AVATAR_,
                    CREATED_AT_ = excluded.CREATED_AT_,
                    UPDATED_AT_ = excluded.UPDATED_AT_,
                    LAST_CHAT_CONTENT_ = excluded.LAST_CHAT_CONTENT_,
                    LAST_CHAT_TIME_ = excluded.LAST_CHAT_TIME_,
                    LAST_RUN_ID_ = excluded.LAST_RUN_ID_
                """)) {
            statement.setString(1, record.chatId);
            statement.setString(2, StringUtils.hasText(record.chatName) ? record.chatName : record.chatId);
            statement.setString(3, StringUtils.hasText(record.firstAgentKey) ? record.firstAgentKey : "");
            statement.setString(4, resolveFirstAgentName(record.firstAgentName, record.firstAgentKey));
            statement.setString(5, nullable(record.agentAvatar));
            statement.setLong(6, record.createdAt > 0 ? record.createdAt : System.currentTimeMillis());
            statement.setLong(7, record.updatedAt > 0 ? record.updatedAt : System.currentTimeMillis());
            statement.setString(8, StringUtils.hasText(record.lastChatContent) ? record.lastChatContent : "");
            statement.setLong(9, record.lastChatTime > 0 ? record.lastChatTime : System.currentTimeMillis());
            statement.setString(10, nullable(record.lastRunId));
            statement.executeUpdate();
        }
    }

    private void upsertNotifyQueue(
            Connection connection,
            String agentKey,
            String chatId,
            String runId,
            long enqueueAt,
            String payloadJson
    ) throws SQLException {
        try (PreparedStatement statement = connection.prepareStatement("""
                INSERT OR IGNORE INTO CHAT_NOTIFY_QUEUE_(
                    AGENT_KEY_, CHAT_ID_, RUN_ID_, EVENT_KIND_, STATE_, ENQUEUE_AT_, ACK_AT_, PAYLOAD_JSON_
                ) VALUES (?, ?, ?, ?, ?, ?, NULL, ?)
                """)) {
            statement.setString(1, agentKey);
            statement.setString(2, chatId);
            statement.setString(3, runId);
            statement.setString(4, EVENT_KIND_RUN_COMPLETED);
            statement.setString(5, EVENT_STATE_PENDING);
            statement.setLong(6, enqueueAt);
            statement.setString(7, payloadJson);
            statement.executeUpdate();
        }
    }

    private long markAllPendingAsAcked(Connection connection, String agentKey, long ackAt) throws SQLException {
        try (PreparedStatement statement = connection.prepareStatement("""
                UPDATE CHAT_NOTIFY_QUEUE_
                SET STATE_ = ?, ACK_AT_ = ?
                WHERE AGENT_KEY_ = ? AND STATE_ = ?
                """)) {
            statement.setString(1, EVENT_STATE_ACKED);
            statement.setLong(2, ackAt);
            statement.setString(3, agentKey);
            statement.setString(4, EVENT_STATE_PENDING);
            return statement.executeUpdate();
        }
    }

    private long countDistinctPendingChats(Connection connection, String agentKey) throws SQLException {
        try (PreparedStatement statement = connection.prepareStatement("""
                SELECT COUNT(DISTINCT CHAT_ID_) AS CNT
                FROM CHAT_NOTIFY_QUEUE_
                WHERE AGENT_KEY_ = ? AND STATE_ = ?
                """)) {
            statement.setString(1, agentKey);
            statement.setString(2, EVENT_STATE_PENDING);
            try (ResultSet resultSet = statement.executeQuery()) {
                if (!resultSet.next()) {
                    return 0L;
                }
                return resultSet.getLong("CNT");
            }
        }
    }

    private void refreshAgentDialogIndex(Connection connection, String agentKey) throws SQLException {
        if (!StringUtils.hasText(agentKey)) {
            return;
        }
        ChatIndexRecord latest;
        try (PreparedStatement latestStatement = connection.prepareStatement("""
                SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_,
                       CREATED_AT_, UPDATED_AT_, LAST_CHAT_CONTENT_, LAST_CHAT_TIME_, LAST_RUN_ID_
                FROM CHAT_INDEX_
                WHERE AGENT_KEY_ = ?
                ORDER BY LAST_CHAT_TIME_ DESC, UPDATED_AT_ DESC
                LIMIT 1
                """)) {
            latestStatement.setString(1, agentKey);
            try (ResultSet latestResult = latestStatement.executeQuery()) {
                if (!latestResult.next()) {
                    return;
                }
                latest = mapChatIndexRecord(latestResult);
            }
        }

        long unread = countDistinctPendingChats(connection, agentKey);
        long updatedAt = System.currentTimeMillis();
        try (PreparedStatement upsert = connection.prepareStatement("""
                INSERT INTO AGENT_DIALOG_INDEX_(
                    AGENT_KEY_, AGENT_NAME_, AGENT_AVATAR_, LATEST_CHAT_ID_, LATEST_CHAT_NAME_,
                    LATEST_CHAT_CONTENT_, LATEST_CHAT_TIME_, UNREAD_CHAT_COUNT_, UPDATED_AT_
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(AGENT_KEY_) DO UPDATE SET
                    AGENT_NAME_ = excluded.AGENT_NAME_,
                    AGENT_AVATAR_ = excluded.AGENT_AVATAR_,
                    LATEST_CHAT_ID_ = excluded.LATEST_CHAT_ID_,
                    LATEST_CHAT_NAME_ = excluded.LATEST_CHAT_NAME_,
                    LATEST_CHAT_CONTENT_ = excluded.LATEST_CHAT_CONTENT_,
                    LATEST_CHAT_TIME_ = excluded.LATEST_CHAT_TIME_,
                    UNREAD_CHAT_COUNT_ = excluded.UNREAD_CHAT_COUNT_,
                    UPDATED_AT_ = excluded.UPDATED_AT_
                """)) {
            upsert.setString(1, latest.firstAgentKey);
            upsert.setString(2, resolveFirstAgentName(latest.firstAgentName, latest.firstAgentKey));
            upsert.setString(3, nullable(latest.agentAvatar));
            upsert.setString(4, nullable(latest.chatId));
            upsert.setString(5, nullable(latest.chatName));
            upsert.setString(6, StringUtils.hasText(latest.lastChatContent) ? latest.lastChatContent : "");
            upsert.setLong(7, latest.lastChatTime > 0 ? latest.lastChatTime : latest.updatedAt);
            upsert.setLong(8, unread);
            upsert.setLong(9, updatedAt);
            upsert.executeUpdate();
        }
    }

    private String normalizeAgentChatSort(String rawSort) {
        String normalized = nullable(rawSort);
        if (normalized == null) {
            return "LATEST_CHAT_TIME_DESC";
        }
        if (!ALLOWED_AGENT_CHAT_SORTS.contains(normalized)) {
            return "LATEST_CHAT_TIME_DESC";
        }
        return normalized;
    }

    private void ensureColumnExists(
            Connection connection,
            String tableName,
            String columnName,
            String columnDefinition
    ) throws SQLException {
        boolean exists = false;
        try (PreparedStatement statement = connection.prepareStatement("PRAGMA table_info(" + tableName + ")");
             ResultSet resultSet = statement.executeQuery()) {
            while (resultSet.next()) {
                if (columnName.equalsIgnoreCase(resultSet.getString("name"))) {
                    exists = true;
                    break;
                }
            }
        }
        if (exists) {
            return;
        }
        try (Statement statement = connection.createStatement()) {
            statement.execute("ALTER TABLE " + tableName + " ADD COLUMN " + columnName + " " + columnDefinition);
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

    private void appendJsonLine(Path path, Object value) {
        if (path == null || value == null) {
            return;
        }
        try {
            Files.createDirectories(path.getParent());
            String line = objectMapper.writeValueAsString(value) + System.lineSeparator();
            Files.writeString(
                    path,
                    line,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.APPEND,
                    StandardOpenOption.WRITE
            );
        } catch (Exception ex) {
            log.warn("Cannot append chat event line path={}", path, ex);
        }
    }

    private boolean isPersistedEventType(String type) {
        return "request.submit".equals(type);
    }

    private String textValue(JsonNode node) {
        if (node == null || node.isNull() || node.isMissingNode() || !node.isTextual()) {
            return null;
        }
        String text = node.asText();
        return StringUtils.hasText(text) ? text.trim() : null;
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
                nullable(record.agentAvatar),
                created
        );
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
            String agentAvatar,
            boolean created
    ) {
    }

    public record RunCompletion(
            String chatId,
            String runId,
            String assistantContent,
            String fallbackUserMessage,
            long completedAt
    ) {
    }

    public record MarkReadResult(
            String agentKey,
            long ackedEvents,
            long ackedChats,
            long unreadChatCount
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
            List<PersistedEvent> persistedEvents,
            int lineIndex
    ) {
    }

    private record PersistedEvent(
            String type,
            long timestamp,
            Map<String, Object> payload,
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
        public String agentAvatar;
        public long createdAt;
        public long updatedAt;
        public String lastChatContent;
        public long lastChatTime;
        public String lastRunId;
    }
}
