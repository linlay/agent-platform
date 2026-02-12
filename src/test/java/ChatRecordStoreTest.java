package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryProperties;
import com.linlay.springaiagw.model.agw.AgwChatDetailResponse;
import com.linlay.springaiagw.model.agw.AgwChatSummaryResponse;
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
                List.of(
                        message("user", "你好", 1707000000000L, null, null, null, null),
                        message("assistant", "你好，我是助手", 1707000000001L, null, null, null, null)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

        assertThat(detail.chatId()).isEqualTo(chatId);
        assertThat(detail.chatName()).isEqualTo("测试会话");
        assertThat(detail.messages()).isNull();
        assertThat(detail.events()).isNotNull();
        assertThat(detail.events()).extracting(item -> item.get("type"))
                .contains("query.message", "run.start", "message.snapshot", "run.complete");
        assertThat(detail.references()).isNull();
    }

    @Test
    void loadChatShouldReturnEventsAndMessagesWhenIncludeRawMessagesIsTrue() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174011";
        String runId = "run_002";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "快照会话", 1707000100000L, 1707000100000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, runRecord(
                chatId,
                runId,
                1707000100000L,
                List.of(
                        message("user", "帮我列目录", 1707000100000L, null, null, null, null),
                        message("assistant", "先执行 ls", 1707000100001L, null, null, null, null),
                        message("assistant", "", 1707000100002L, "bash", "tool_call_1", Map.of("command", "ls"), null),
                        message("tool", "{\"ok\":true}", 1707000100003L, "bash", "tool_call_1", Map.of("command", "ls"), "{\"ok\":true}")
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, true);

        assertThat(detail.messages()).isNotNull();
        assertThat(detail.messages()).hasSize(4);
        assertThat(detail.messages().getFirst().get("role")).isEqualTo("user");
        assertThat(detail.events()).isNotNull();
        assertThat(detail.events()).extracting(item -> item.get("type"))
                .contains("query.message", "chat.start", "run.start", "message.snapshot", "tool.snapshot", "run.complete");

        Map<String, Object> messageSnapshot = findFirstEvent(detail.events(), "message.snapshot");
        assertThat(messageSnapshot).containsKeys("type", "messageId", "text", "timestamp", "contentId");
        assertThat(messageSnapshot.get("text")).isEqualTo("先执行 ls");
        assertThat(messageSnapshot).doesNotContainKey("reasoningId");

        Map<String, Object> toolSnapshot = findFirstEvent(detail.events(), "tool.snapshot");
        assertThat(toolSnapshot).containsKeys(
                "toolId",
                "toolName",
                "contentId",
                "toolType",
                "toolApi",
                "toolParams",
                "description",
                "arguments",
                "timestamp"
        );
        assertThat(toolSnapshot.get("toolId")).isEqualTo(runId + "_2");
        assertThat(toolSnapshot).doesNotContainKey("result");
    }

    @Test
    void loadChatShouldCollectReferencesFromRecordTypeLines() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174012";
        Path chatDir = tempDir.resolve("chats");
        writeIndex(chatDir, chatId, "引用会话", 1707000200000L, 1707000200000L);

        Path historyPath = chatDir.resolve(chatId + ".json");
        writeJsonLine(historyPath, Map.of(
                "recordType", "request.query",
                "references", List.of(
                        Map.of("id", "ref_001", "type", "url", "name", "A", "url", "https://example.com/a"),
                        Map.of("id", "ref_001", "type", "url", "name", "B", "url", "https://example.com/b")
                )
        ));
        writeJsonLine(historyPath, runRecord(
                chatId,
                "run_003",
                1707000200000L,
                List.of(
                        message("user", "你好", 1707000200000L, null, null, null, null),
                        message("assistant", "你好", 1707000200001L, null, null, null, null)
                )
        ));

        ChatRecordStore store = newStore();
        AgwChatDetailResponse detail = store.loadChat(chatId, false);

        assertThat(detail.messages()).isNull();
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
            List<Map<String, Object>> messages
    ) {
        Map<String, Object> record = new LinkedHashMap<>();
        record.put("v", 1);
        record.put("chatId", chatId);
        record.put("runId", runId);
        record.put("updatedAt", updatedAt);
        record.put("messages", messages);
        return record;
    }

    private Map<String, Object> message(
            String role,
            String content,
            long ts,
            String name,
            String toolCallId,
            Object toolArgs,
            String toolResult
    ) {
        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", role);
        message.put("content", content);
        message.put("ts", ts);
        message.put("name", name);
        message.put("toolCallId", toolCallId);
        message.put("toolArgs", toolArgs);
        message.put("toolResult", toolResult);
        return message;
    }

    private Map<String, Object> findFirstEvent(List<Map<String, Object>> events, String type) {
        List<Map<String, Object>> matched = new ArrayList<>();
        for (Map<String, Object> event : events) {
            if (type.equals(event.get("type"))) {
                matched.add(event);
            }
        }
        assertThat(matched).isNotEmpty();
        return matched.getFirst();
    }
}
