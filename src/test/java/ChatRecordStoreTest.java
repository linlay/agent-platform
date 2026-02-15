package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryProperties;
import com.linlay.springaiagw.model.api.AgwChatDetailResponse;
import com.linlay.springaiagw.model.api.AgwChatSummaryResponse;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
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
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_001",
                1707000000000L,
                query("run_001", chatId, "你好", List.of()),
                List.of(
                        userMessage("你好", 1707000000000L),
                        assistantContentMessage("你好，我是助手", 1707000000001L)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

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
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_002",
                1707000100000L,
                query("run_002", chatId, "帮我列目录", List.of()),
                List.of(
                        userMessage("帮我列目录", 1707000100000L),
                        assistantReasoningMessage("先判断是否需要工具", 1707000100001L),
                        assistantToolCallMessage("bash", "call_tool_1", "{\"command\":\"ls\"}", 1707000100002L, "tool_short_1", null),
                        toolMessage("bash", "call_tool_1", "{\"ok\":true}", 1707000100003L, "tool_short_1", null),
                        assistantToolCallMessage("switch_frontend_theme", "call_tool_2", "{\"theme\":\"dark\"}", 1707000100004L, null, "action_short_1"),
                        toolMessage("switch_frontend_theme", "call_tool_2", "OK", 1707000100005L, null, "action_short_1"),
                        assistantContentMessage("处理完成", 1707000100006L)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, true);

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
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_003",
                1707000200000L,
                query("run_003", chatId, "你好", List.of(
                        Map.of("id", "ref_001", "type", "url", "name", "A", "url", "https://example.com/a"),
                        Map.of("id", "ref_001", "type", "url", "name", "B", "url", "https://example.com/b")
                )),
                List.of(
                        userMessage("你好", 1707000200000L),
                        assistantContentMessage("你好", 1707000200001L)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

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
        Path chatDir = tempDir.resolve("chats");
        Path indexPath = chatDir.resolve("_chats.jsonl");
        writeJsonLine(indexPath, Map.of(
                "chatId", firstChat,
                "chatName", "legacy",
                "firstAgentKey", "demo",
                "createdAt", 100L
        ));
        writeJsonLine(indexPath, Map.of(
                "chatId", secondChat,
                "chatName", "active",
                "firstAgentKey", "demo",
                "createdAt", 50L,
                "updatedAt", 200L
        ));

        ChatRecordStore store = newStore();
        List<AgwChatSummaryResponse> chats = store.listChats();

        assertThat(chats).hasSize(2);
        assertThat(chats.getFirst().chatId()).isEqualTo(secondChat);
        assertThat(chats.getFirst().updatedAt()).isEqualTo(200L);
        assertThat(chats.get(1).chatId()).isEqualTo(firstChat);
        assertThat(chats.get(1).updatedAt()).isEqualTo(100L);
    }

    @Test
    void ensureChatShouldRefreshUpdatedAtAndRewriteDuplicateRecords() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174015";
        Path chatDir = tempDir.resolve("chats");
        Path indexPath = chatDir.resolve("_chats.jsonl");
        writeJsonLine(indexPath, Map.of(
                "chatId", chatId,
                "chatName", "dup-1",
                "firstAgentKey", "demo",
                "createdAt", 1000L,
                "updatedAt", 1000L
        ));
        writeJsonLine(indexPath, Map.of(
                "chatId", chatId,
                "chatName", "dup-2",
                "firstAgentKey", "demo",
                "createdAt", 1000L,
                "updatedAt", 1000L
        ));

        ChatRecordStore store = newStore();
        store.ensureChat(chatId, "demoAgent", "后续消息");

        List<String> lines = Files.readAllLines(indexPath).stream().filter(line -> !line.isBlank()).toList();
        assertThat(lines).hasSize(1);
        Map<String, Object> record = objectMapper.readValue(lines.getFirst(), Map.class);
        assertThat(record.get("chatId")).isEqualTo(chatId);
        assertThat(((Number) record.get("createdAt")).longValue()).isEqualTo(1000L);
        assertThat(((Number) record.get("updatedAt")).longValue()).isGreaterThanOrEqualTo(1000L);
    }

    @Test
    void loadChatShouldEmitChatStartOnlyOnceAcrossMultipleRuns() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174016";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "多轮会话", 1707000300000L, 1707000400000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_004",
                1707000300000L,
                query("run_004", chatId, "第一轮", List.of()),
                List.of(
                        userMessage("第一轮", 1707000300000L),
                        assistantContentMessage("第一轮回答", 1707000300001L)
                )
        ));
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_005",
                1707000400000L,
                query("run_005", chatId, "第二轮", List.of()),
                List.of(
                        userMessage("第二轮", 1707000400000L),
                        assistantContentMessage("第二轮回答", 1707000400001L)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

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
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_006",
                1707000500000L,
                query("run_006", chatId, "第一轮", List.of()),
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
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

        Map<String, Object> planUpdate = detail.events().stream()
                .filter(event -> "plan.update".equals(event.get("type")))
                .findFirst()
                .orElseThrow();

        assertThat(planUpdate).containsEntry("type", "plan.update");
        assertThat(planUpdate).containsEntry("planId", "plan_chat_001");
        assertThat(planUpdate).containsEntry("chatId", chatId);
        assertThat(planUpdate).containsKey("plan");
        assertThat(planUpdate).containsKey("timestamp");
        assertThat(planUpdate).containsEntry("rawEvent", null);
        assertThat(planUpdate).doesNotContainKey("seq");
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
        return new ChatRecordStore(objectMapper, properties);
    }

    private void writeIndex(Path chatDir, String chatId, String chatName, long createdAt, long updatedAt) throws Exception {
        Files.createDirectories(chatDir);
        Path indexPath = chatDir.resolve("_chats.jsonl");
        writeJsonLine(indexPath, Map.of(
                "chatId", chatId,
                "chatName", chatName,
                "firstAgentKey", "demo",
                "createdAt", createdAt,
                "updatedAt", updatedAt
        ));
    }

    private void writeJsonLine(Path path, Object value) throws Exception {
        Files.createDirectories(path.getParent());
        String line = objectMapper.writeValueAsString(value) + System.lineSeparator();
        Files.writeString(path, line, java.nio.file.StandardOpenOption.CREATE, java.nio.file.StandardOpenOption.APPEND);
    }

    private Map<String, Object> runRecord(
            String chatId,
            String runId,
            long updatedAt,
            Map<String, Object> query,
            List<Map<String, Object>> messages
    ) {
        return runRecord(chatId, runId, updatedAt, query, messages, null);
    }

    private Map<String, Object> runRecord(
            String chatId,
            String runId,
            long updatedAt,
            Map<String, Object> query,
            List<Map<String, Object>> messages,
            Map<String, Object> planSnapshot
    ) {
        Map<String, Object> record = new LinkedHashMap<>();
        record.put("chatId", chatId);
        record.put("runId", runId);
        record.put("transactionId", runId);
        record.put("updatedAt", updatedAt);
        record.put("query", query);
        record.put("messages", messages);
        if (planSnapshot != null && !planSnapshot.isEmpty()) {
            record.put("planSnapshot", planSnapshot);
        }
        return record;
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
