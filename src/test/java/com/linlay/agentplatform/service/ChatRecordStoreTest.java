package com.linlay.agentplatform.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.memory.ChatWindowMemoryProperties;
import com.linlay.agentplatform.model.api.ChatDetailResponse;
import com.linlay.agentplatform.model.api.ChatSummaryResponse;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class ChatRecordStoreTest {

    @TempDir
    Path tempDir;

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void loadChatShouldReturnEventsOnlyWhenIncludeRawMessagesIsFalse() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174010";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "测试会话", 1707000000000L, 1707000000000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_001", query("run_001", chatId, "你好", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_001", "oneshot", 1, null,
                1707000000000L,
                List.of(
                        userMessage("你好", 1707000000000L),
                        assistantContentMessage("你好，我是助手", 1707000000001L)
                )));

        ChatRecordStore store = newStore();
        ChatDetailResponse detail = store.loadChat(chatId, false);

        assertThat(detail.chatId()).isEqualTo(chatId);
        assertThat(detail.chatName()).isEqualTo("测试会话");
        assertThat(detail.rawMessages()).isNull();
        assertThat(detail.events()).isNotNull();
        assertThat(detail.events()).extracting(item -> item.get("type"))
                .contains(
                        "request.query",
                        "chat.start",
                        "run.start",
                        "content.snapshot",
                        "run.complete"
                );
        assertThat(detail.events().getFirst()).containsKey("seq");
        assertThat(detail.references()).isNull();
    }

    @Test
    void loadChatShouldReturnRawMessagesAndSnapshotEventsWhenIncludeRawMessagesIsTrue() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174011";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "快照会话", 1707000100000L, 1707000100000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_002", query("run_002", chatId, "帮我列目录", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_002", "react", 1, null,
                1707000100000L,
                List.of(
                        userMessage("帮我列目录", 1707000100000L),
                        assistantReasoningMessage("先判断是否需要工具", 1707000100001L),
                        assistantToolCallMessage("bash", "call_tool_1", "{\"command\":\"ls\"}", 1707000100002L, "tool_short_1", null),
                        toolMessage("bash", "call_tool_1", "{\"ok\":true}", 1707000100003L, "tool_short_1", null)
                )));
        writeJsonLine(historyPath, stepLine(chatId, "run_002", "react", 2, null,
                1707000100004L,
                List.of(
                        assistantToolCallMessage("switch_frontend_theme", "call_tool_2", "{\"theme\":\"dark\"}", 1707000100004L, null, "action_short_1"),
                        toolMessage("switch_frontend_theme", "call_tool_2", "OK", 1707000100005L, null, "action_short_1"),
                        assistantContentMessage("处理完成", 1707000100006L)
                )));

        ChatRecordStore store = newStore();
        ChatDetailResponse detail = store.loadChat(chatId, true);

        assertThat(detail.rawMessages()).isNotNull();
        assertThat(detail.rawMessages()).hasSize(7);
        assertThat(detail.rawMessages().getFirst().get("role")).isEqualTo("user");

        assertThat(detail.events()).isNotNull();
        assertThat(detail.events()).extracting(item -> item.get("type")).contains(
                "request.query",
                "chat.start",
                "run.start",
                "reasoning.snapshot",
                "tool.snapshot",
                "tool.result",
                "action.snapshot",
                "action.result",
                "content.snapshot",
                "run.complete"
        );

        assertThat(countType(detail.events(), "tool.start")).isEqualTo(0);
        assertThat(countType(detail.events(), "tool.args")).isEqualTo(0);
        assertThat(countType(detail.events(), "tool.end")).isEqualTo(0);
        assertThat(countType(detail.events(), "action.start")).isEqualTo(0);
        assertThat(countType(detail.events(), "action.args")).isEqualTo(0);
        assertThat(countType(detail.events(), "action.end")).isEqualTo(0);
    }

    @Test
    void loadChatShouldCollectReferencesFromRunQuery() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174012";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "引用会话", 1707000200000L, 1707000200000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_003", query("run_003", chatId, "你好", List.of(
                Map.of("id", "ref_001", "type", "url", "name", "A", "url", "https://example.com/a"),
                Map.of("id", "ref_001", "type", "url", "name", "B", "url", "https://example.com/b")
        ))));
        writeJsonLine(historyPath, stepLine(chatId, "run_003", "oneshot", 1, null,
                1707000200000L,
                List.of(
                        userMessage("你好", 1707000200000L),
                        assistantContentMessage("你好", 1707000200001L)
                )));

        ChatRecordStore store = newStore();
        ChatDetailResponse detail = store.loadChat(chatId, false);

        assertThat(detail.rawMessages()).isNull();
        assertThat(detail.events()).isNotNull();
        assertThat(detail.references()).isNotNull();
        assertThat(detail.references()).hasSize(1);
        assertThat(detail.references().getFirst().id()).isEqualTo("ref_001");
    }

    @Test
    void listChatsShouldUseUpdatedAtAndFallbackToCreatedAtForLegacyRecords() throws Exception {
        String firstChat = "123e4567-e89b-12d3-a456-426614174013";
        String secondChat = "123e4567-e89b-12d3-a456-426614174014";
        ChatRecordStore store = newStore();
        store.ensureChat(firstChat, "demo", "Demo Agent", "legacy");
        store.ensureChat(secondChat, "demo", "Demo Agent", "active");
        store.onRunCompleted(new ChatRecordStore.RunCompletion(firstChat, "run-1", "legacy", "legacy", 1000L));
        store.onRunCompleted(new ChatRecordStore.RunCompletion(secondChat, "run-2", "active", "active", 2000L));
        List<ChatSummaryResponse> chats = store.listChats();

        assertThat(chats).hasSize(2);
        assertThat(chats.getFirst().chatId()).isEqualTo(secondChat);
        assertThat(chats.getFirst().chatName()).isEqualTo("active");
        assertThat(chats.getFirst().agentKey()).isEqualTo("demo");
        assertThat(chats.getFirst().updatedAt()).isGreaterThan(0L);
        assertThat(chats.getFirst().lastRunId()).isEqualTo("run-2");
        assertThat(chats.getFirst().readStatus()).isEqualTo(0);
        assertThat(chats.get(1).chatId()).isEqualTo(firstChat);
        assertThat(chats.get(1).chatName()).isEqualTo("legacy");
        assertThat(chats.get(1).agentKey()).isEqualTo("demo");
        assertThat(chats.get(1).updatedAt()).isGreaterThan(0L);
        assertThat(chats.get(1).lastRunId()).isEqualTo("run-1");
    }

    @Test
    void ensureChatShouldRefreshUpdatedAtAndRewriteDuplicateRecords() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174015";
        ChatRecordStore store = newStore();
        ChatRecordStore.ChatSummary first = store.ensureChat(chatId, "demo", "Demo Agent", "dup-1");
        ChatRecordStore.ChatSummary second = store.ensureChat(chatId, "other", "Other Agent", "dup-2");

        assertThat(first.created()).isTrue();
        assertThat(second.created()).isFalse();
        assertThat(second.agentKey()).isEqualTo("demo");
        assertThat(second.updatedAt()).isGreaterThanOrEqualTo(first.updatedAt());
    }

    @Test
    void initializeDatabaseShouldIgnoreLegacyJsonlIndex() throws Exception {
        Path chatDir = tempDir.resolve("chats");
        Files.createDirectories(chatDir);
        Files.writeString(chatDir.resolve("_chats.jsonl"), """
                {"chatId":"123e4567-e89b-12d3-a456-426614174019","chatName":"legacy-index"}
                """);

        ChatRecordStore store = newStore();
        List<String> tables = listTables(chatDir.resolve("chats.db"));
        assertThat(tables).contains("CHATS");
        assertThat(tables).doesNotContain("CHAT_INDEX_", "CHAT_NOTIFY_QUEUE_", "AGENT_DIALOG_INDEX_");
        assertThat(store.listChats()).isEmpty();

        store.ensureChat("123e4567-e89b-12d3-a456-426614174020", "demo", "Demo Agent", "fresh");
        assertThat(store.listChats()).hasSize(1);
        assertThat(store.listChats().getFirst().chatId()).isEqualTo("123e4567-e89b-12d3-a456-426614174020");
    }

    @Test
    void markChatReadShouldUpdateReadStatusAndReadAt() {
        String chatId = "123e4567-e89b-12d3-a456-426614174091";
        ChatRecordStore store = newStore();
        store.ensureChat(chatId, "agent-a", "Agent A", "hello a");
        store.onRunCompleted(new ChatRecordStore.RunCompletion(chatId, "run-a", "reply a", "hello a", System.currentTimeMillis()));

        List<ChatSummaryResponse> unreadChats = store.listChats();
        assertThat(unreadChats.getFirst().readStatus()).isEqualTo(0);
        assertThat(unreadChats.getFirst().readAt()).isNull();

        ChatRecordStore.MarkChatReadResult readResult = store.markChatRead(chatId);
        assertThat(readResult.chatId()).isEqualTo(chatId);
        assertThat(readResult.readStatus()).isEqualTo(1);
        assertThat(readResult.readAt()).isNotNull();

        List<ChatSummaryResponse> readChats = store.listChats();
        assertThat(readChats.getFirst().readStatus()).isEqualTo(1);
        assertThat(readChats.getFirst().readAt()).isNotNull();
    }

    @Test
    void listChatsShouldSupportIncrementalQueryByLastRunId() {
        String chatA = "123e4567-e89b-12d3-a456-426614174093";
        String chatB = "123e4567-e89b-12d3-a456-426614174094";
        ChatRecordStore store = newStore();
        store.ensureChat(chatA, "agent-a", "Agent A", "hello a");
        store.ensureChat(chatB, "agent-b", "Agent B", "hello b");
        store.onRunCompleted(new ChatRecordStore.RunCompletion(chatA, "a1", "reply a", "hello a", System.currentTimeMillis()));
        store.onRunCompleted(new ChatRecordStore.RunCompletion(chatB, "a2", "reply b", "hello b", System.currentTimeMillis() + 1));

        List<ChatSummaryResponse> incremental = store.listChats("a1");
        assertThat(incremental).hasSize(1);
        assertThat(incremental.getFirst().chatId()).isEqualTo(chatB);
        assertThat(incremental.getFirst().lastRunId()).isEqualTo("a2");
    }

    @Test
    void loadChatShouldEmitChatStartOnlyOnceAcrossMultipleRuns() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174016";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "多轮会话", 1707000300000L, 1707000400000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_004", query("run_004", chatId, "第一轮", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_004", "oneshot", 1, null,
                1707000300000L,
                List.of(
                        userMessage("第一轮", 1707000300000L),
                        assistantContentMessage("第一轮回答", 1707000300001L)
                )));
        writeJsonLine(historyPath, queryLine(chatId, "run_005", query("run_005", chatId, "第二轮", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_005", "oneshot", 1, null,
                1707000400000L,
                List.of(
                        userMessage("第二轮", 1707000400000L),
                        assistantContentMessage("第二轮回答", 1707000400001L)
                )));

        ChatRecordStore store = newStore();
        ChatDetailResponse detail = store.loadChat(chatId, false);

        assertThat(detail.events()).isNotNull();
        assertThat(countType(detail.events(), "chat.start")).isEqualTo(1);
        assertThat(countType(detail.events(), "run.start")).isEqualTo(2);
        assertThat(countType(detail.events(), "run.complete")).isEqualTo(2);
        assertThat(countType(detail.events(), "chat.update")).isEqualTo(0);
    }

    @Test
    void loadChatShouldReplayPlanUpdateInStrictFormat() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174017";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "计划会话", 1707000500000L, 1707000500000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_006", query("run_006", chatId, "第一轮", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_006", "plan", 1, null,
                1707000500000L,
                List.of(
                        userMessage("第一轮", 1707000500000L),
                        assistantContentMessage("已创建计划", 1707000500001L)
                ),
                Map.of(
                        "planId", "plan_chat_001",
                        "plan", List.of(
                                Map.of("taskId", "task0", "description", "收集信息", "status", "init"),
                                Map.of("taskId", "task1", "description", "执行任务", "status", "in_progress")
                        )
                )));

        ChatRecordStore store = newStore();
        ChatDetailResponse detail = store.loadChat(chatId, false);

        Map<String, Object> planUpdate = detail.events().stream()
                .filter(event -> "plan.update".equals(event.get("type")))
                .findFirst()
                .orElseThrow();

        assertThat(planUpdate).containsEntry("type", "plan.update");
        assertThat(planUpdate).containsEntry("planId", "plan_chat_001");
        assertThat(planUpdate).containsEntry("chatId", chatId);
        assertThat(planUpdate).containsKey("plan");
        assertThat(planUpdate).containsKey("timestamp");
        assertThat(planUpdate).doesNotContainKey("seq");
    }

    @Test
    void appendEventShouldPersistRequestSubmitForHistoryReplay() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174018";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "提交会话", 1707000600000L, 1707000600000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, queryLine(chatId, "run_007", query("run_007", chatId, "第一轮", List.of())));
        writeJsonLine(historyPath, stepLine(chatId, "run_007", "oneshot", 1, null,
                1707000600000L,
                List.of(
                        userMessage("第一轮", 1707000600000L),
                        assistantContentMessage("等待确认", 1707000600001L)
                )));

        ChatRecordStore store = newStore();
        store.appendEvent(chatId, objectMapper.writeValueAsString(Map.of(
                "seq", 99,
                "type", "request.submit",
                "timestamp", 1707000600002L,
                "requestId", "req_submit_001",
                "chatId", chatId,
                "runId", "run_007",
                "toolId", "call_frontend_1",
                "payload", Map.of("confirmed", true)
        )));

        ChatDetailResponse detail = store.loadChat(chatId, false);
        Map<String, Object> requestSubmit = detail.events().stream()
                .filter(event -> "request.submit".equals(event.get("type")))
                .findFirst()
                .orElseThrow();

        assertThat(requestSubmit).containsEntry("requestId", "req_submit_001");
        assertThat(requestSubmit).containsEntry("chatId", chatId);
        assertThat(requestSubmit).containsEntry("runId", "run_007");
        assertThat(requestSubmit).containsEntry("toolId", "call_frontend_1");
        assertThat(requestSubmit).containsKey("seq");
    }

    @Test
    void loadChatShouldRejectInvalidChatId() {
        ChatRecordStore store = newStore();
        assertThatThrownBy(() -> store.loadChat("not-a-uuid", false))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("chatId must be a valid UUID");
    }

    @Test
    void loadChatShouldThrowNotFoundForMissingChat() {
        ChatRecordStore store = newStore();
        assertThatThrownBy(() -> store.loadChat("123e4567-e89b-12d3-a456-426614174099", false))
                .isInstanceOf(ChatNotFoundException.class);
    }

    private ChatRecordStore newStore() {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.getIndex().setSqliteFile(tempDir.resolve("chats").resolve("chats.db").toString());
        ChatRecordStore store = new ChatRecordStore(objectMapper, properties);
        store.initializeDatabase();
        return store;
    }

    private void writeIndex(Path chatDir, String chatId, String chatName, long createdAt, long updatedAt) throws Exception {
        Files.createDirectories(chatDir);
            Path dbPath = chatDir.resolve("chats.db");
            try (java.sql.Connection connection = java.sql.DriverManager.getConnection("jdbc:sqlite:" + dbPath);
                 java.sql.Statement statement = connection.createStatement()) {
            statement.execute("""
                    CREATE TABLE IF NOT EXISTS CHATS (
                      CHAT_ID_ TEXT PRIMARY KEY,
                      CHAT_NAME_ TEXT NOT NULL,
                      AGENT_KEY_ TEXT NOT NULL,
                      CREATED_AT_ INTEGER NOT NULL,
                      UPDATED_AT_ INTEGER NOT NULL,
                      LAST_RUN_ID_ VARCHAR(12) NOT NULL,
                      LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
                      READ_STATUS_ INTEGER NOT NULL DEFAULT 1,
                      READ_AT_ INTEGER
                    )
                    """);
            try (java.sql.PreparedStatement upsert = connection.prepareStatement("""
                    INSERT INTO CHATS(
                        CHAT_ID_, CHAT_NAME_, AGENT_KEY_,
                        CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_
                    ) VALUES (?, ?, ?, ?, ?, '', '', 1, ?)
                    ON CONFLICT(CHAT_ID_) DO UPDATE SET
                        CHAT_NAME_ = excluded.CHAT_NAME_,
                        AGENT_KEY_ = excluded.AGENT_KEY_,
                        CREATED_AT_ = excluded.CREATED_AT_,
                        UPDATED_AT_ = excluded.UPDATED_AT_
                    """)) {
                upsert.setString(1, chatId);
                upsert.setString(2, chatName);
                upsert.setString(3, "demo");
                upsert.setLong(4, createdAt);
                upsert.setLong(5, updatedAt);
                upsert.setLong(6, updatedAt);
                upsert.executeUpdate();
            }
        }
    }

    private void writeJsonLine(Path path, Object value) throws Exception {
        Files.createDirectories(path.getParent());
        String line = objectMapper.writeValueAsString(value) + System.lineSeparator();
        Files.writeString(path, line, java.nio.file.StandardOpenOption.CREATE, java.nio.file.StandardOpenOption.APPEND);
    }

    private List<String> listTables(Path dbPath) throws Exception {
        try (java.sql.Connection connection = java.sql.DriverManager.getConnection("jdbc:sqlite:" + dbPath);
             java.sql.PreparedStatement statement = connection.prepareStatement("""
                     SELECT name
                     FROM sqlite_master
                     WHERE type = 'table'
                     """);
             java.sql.ResultSet resultSet = statement.executeQuery()) {
            List<String> names = new java.util.ArrayList<>();
            while (resultSet.next()) {
                names.add(resultSet.getString("name"));
            }
            return names;
        }
    }

    private Map<String, Object> queryLine(String chatId, String runId, Map<String, Object> query) {
        Map<String, Object> line = new LinkedHashMap<>();
        line.put("_type", "query");
        line.put("chatId", chatId);
        line.put("runId", runId);
        line.put("updatedAt", System.currentTimeMillis());
        line.put("query", query);
        return line;
    }

    private Map<String, Object> stepLine(
            String chatId,
            String runId,
            String stage,
            int seq,
            String taskId,
            long updatedAt,
            List<Map<String, Object>> messages
    ) {
        return stepLine(chatId, runId, stage, seq, taskId, updatedAt, messages, null);
    }

    private Map<String, Object> stepLine(
            String chatId,
            String runId,
            String stage,
            int seq,
            String taskId,
            long updatedAt,
            List<Map<String, Object>> messages,
            Map<String, Object> planSnapshot
    ) {
        Map<String, Object> line = new LinkedHashMap<>();
        line.put("_type", "step");
        line.put("chatId", chatId);
        line.put("runId", runId);
        line.put("_stage", stage);
        line.put("_seq", seq);
        if (taskId != null) {
            line.put("taskId", taskId);
        }
        line.put("updatedAt", updatedAt);
        line.put("messages", messages);
        if (planSnapshot != null && !planSnapshot.isEmpty()) {
            line.put("planSnapshot", planSnapshot);
        }
        return line;
    }

    private Map<String, Object> query(
            String requestId,
            String chatId,
            String message,
            List<Map<String, Object>> references
    ) {
        Map<String, Object> query = new LinkedHashMap<>();
        query.put("requestId", requestId);
        query.put("chatId", chatId);
        query.put("agentKey", "demo");
        query.put("role", "user");
        query.put("message", message);
        query.put("references", references);
        query.put("params", Map.of("k", "v"));
        query.put("scene", Map.of("url", "https://example.com", "title", "demo"));
        query.put("stream", true);
        return query;
    }

    private Map<String, Object> userMessage(String text, long ts) {
        return Map.of(
                "role", "user",
                "content", List.of(textPart(text)),
                "ts", ts
        );
    }

    private Map<String, Object> assistantReasoningMessage(String text, long ts) {
        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", "assistant");
        message.put("reasoning_content", List.of(textPart(text)));
        message.put("ts", ts);
        message.put("_reasoningId", "reasoning_short_1");
        message.put("_timing", 12);
        message.put("_usage", usage());
        return message;
    }

    private Map<String, Object> assistantContentMessage(String text, long ts) {
        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", "assistant");
        message.put("content", List.of(textPart(text)));
        message.put("ts", ts);
        message.put("_contentId", "content_short_1");
        message.put("_timing", 12);
        message.put("_usage", usage());
        return message;
    }

    private Map<String, Object> assistantToolCallMessage(
            String toolName,
            String toolCallId,
            String arguments,
            long ts,
            String toolId,
            String actionId
    ) {
        Map<String, Object> function = new LinkedHashMap<>();
        function.put("name", toolName);
        function.put("arguments", arguments);

        Map<String, Object> call = new LinkedHashMap<>();
        call.put("id", toolCallId);
        call.put("type", "function");
        call.put("function", function);
        if (toolId != null) {
            call.put("_toolId", toolId);
        }
        if (actionId != null) {
            call.put("_actionId", actionId);
        }

        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", "assistant");
        message.put("tool_calls", List.of(call));
        message.put("ts", ts);
        message.put("_timing", 20);
        message.put("_usage", usage());
        return message;
    }

    private Map<String, Object> toolMessage(
            String toolName,
            String toolCallId,
            String result,
            long ts,
            String toolId,
            String actionId
    ) {
        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", "tool");
        message.put("name", toolName);
        message.put("tool_call_id", toolCallId);
        message.put("content", List.of(textPart(result)));
        message.put("ts", ts);
        message.put("_timing", 8);
        if (toolId != null) {
            message.put("_toolId", toolId);
        }
        if (actionId != null) {
            message.put("_actionId", actionId);
        }
        return message;
    }

    private Map<String, Object> textPart(String text) {
        return Map.of("type", "text", "text", text);
    }

    private Map<String, Object> usage() {
        Map<String, Object> usage = new LinkedHashMap<>();
        usage.put("input_tokens", null);
        usage.put("output_tokens", null);
        usage.put("total_tokens", null);
        return usage;
    }

    private long countType(List<Map<String, Object>> events, String type) {
        return events.stream().filter(event -> type.equals(event.get("type"))).count();
    }
}
