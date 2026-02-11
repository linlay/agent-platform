package com.linlay.springaiagw.memory;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.ai.chat.messages.UserMessage;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

class ChatWindowMemoryStoreTest {

    @TempDir
    Path tempDir;

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void shouldPersistToolCallArgumentsAndResult() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(20);
        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);

        String chatId = "123e4567-e89b-12d3-a456-426614174000";
        store.appendRun(
                chatId,
                "run_001",
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("请查看目录"),
                        ChatWindowMemoryStore.RunMessage.assistantToolCall("bash", "call_bash_1", "{\"command\":\"ls\"}"),
                        ChatWindowMemoryStore.RunMessage.toolResult("bash", "call_bash_1", "{\"command\":\"ls\"}", "{\"ok\":true,\"command\":\"ls\"}"),
                        ChatWindowMemoryStore.RunMessage.assistantContent("目录检查完成")
                )
        );

        Path file = tempDir.resolve("chats").resolve(chatId + ".json");
        assertThat(Files.exists(file)).isTrue();
        List<String> lines = Files.readAllLines(file).stream().filter(line -> !line.isBlank()).toList();
        assertThat(lines).hasSize(1);
        assertThat(lines.getFirst()).doesNotContain("\n");

        JsonNode root = objectMapper.readTree(lines.getFirst());
        assertThat(root.path("chatId").asText()).isEqualTo(chatId);
        assertThat(root.path("runId").asText()).isEqualTo("run_001");
        assertThat(root.path("messages")).hasSize(4);

        JsonNode assistantToolCall = root.path("messages").get(1);
        assertThat(assistantToolCall.path("role").asText()).isEqualTo("assistant");
        assertThat(assistantToolCall.path("name").asText()).isEqualTo("bash");
        assertThat(assistantToolCall.path("toolCallId").asText()).isEqualTo("call_bash_1");
        assertThat(assistantToolCall.path("toolArgs").path("command").asText()).isEqualTo("ls");

        JsonNode toolResult = root.path("messages").get(2);
        assertThat(toolResult.path("role").asText()).isEqualTo("tool");
        assertThat(toolResult.path("toolResult").isTextual()).isTrue();
        assertThat(toolResult.path("toolResult").asText()).isEqualTo("{\"ok\":true,\"command\":\"ls\"}");

        List<Message> historyMessages = store.loadHistoryMessages(chatId);
        assertThat(historyMessages).hasSize(4);
        assertThat(historyMessages.get(0)).isInstanceOf(UserMessage.class);
        assertThat(historyMessages.get(1)).isInstanceOf(AssistantMessage.class);
        assertThat(historyMessages.get(2)).isInstanceOf(ToolResponseMessage.class);
        assertThat(historyMessages.get(3)).isInstanceOf(AssistantMessage.class);
    }

    @Test
    void shouldTrimToConfiguredWindowSize() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(2);
        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);

        String chatId = "123e4567-e89b-12d3-a456-426614174001";
        store.appendRun(
                chatId,
                "run_001",
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u1"),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a1")
                )
        );
        store.appendRun(
                chatId,
                "run_002",
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u2"),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a2")
                )
        );
        store.appendRun(
                chatId,
                "run_003",
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u3"),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a3")
                )
        );

        Path file = tempDir.resolve("chats").resolve(chatId + ".json");
        List<String> lines = Files.readAllLines(file).stream().filter(line -> !line.isBlank()).toList();
        assertThat(lines).hasSize(2);
        assertThat(objectMapper.readTree(lines.get(0)).path("runId").asText()).isEqualTo("run_002");
        assertThat(objectMapper.readTree(lines.get(1)).path("runId").asText()).isEqualTo("run_003");

        List<Message> historyMessages = store.loadHistoryMessages(chatId);
        assertThat(historyMessages).hasSize(4);
        assertThat(((UserMessage) historyMessages.get(0)).getText()).isEqualTo("u2");
        assertThat(((AssistantMessage) historyMessages.get(1)).getText()).isEqualTo("a2");
        assertThat(((UserMessage) historyMessages.get(2)).getText()).isEqualTo("u3");
        assertThat(((AssistantMessage) historyMessages.get(3)).getText()).isEqualTo("a3");
    }
}
