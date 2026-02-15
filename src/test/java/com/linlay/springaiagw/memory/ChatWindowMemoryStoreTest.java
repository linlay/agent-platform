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
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class ChatWindowMemoryStoreTest {

    @TempDir
    Path tempDir;

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void shouldPersistRunAsJsonLineWithV2Schema() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(20);
        properties.setActionTools(List.of("switch_frontend_theme"));

        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);

        String chatId = "123e4567-e89b-12d3-a456-426614174000";
        store.appendRun(
                chatId,
                "run_001",
                query("run_001", chatId, "请帮我切换主题"),
                systemSnapshot("gpt-5.2", "你是一个有用的助手", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("请帮我切换主题", 1770945237570L),
                        ChatWindowMemoryStore.RunMessage.assistantReasoning("准备调用前端动作", 1770945237571L, 10L, null),
                        ChatWindowMemoryStore.RunMessage.assistantToolCall(
                                "switch_frontend_theme",
                                "call_tool_1",
                                "{\"theme\":\"dark\"}",
                                1770945237572L,
                                50L,
                                null
                        ),
                        ChatWindowMemoryStore.RunMessage.toolResult(
                                "switch_frontend_theme",
                                "call_tool_1",
                                "OK",
                                1770945237573L,
                                5L
                        ),
                        ChatWindowMemoryStore.RunMessage.assistantContent("已切换到 dark 主题", 1770945237574L, 20L, null)
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
        assertThat(root.path("transactionId").asText()).isEqualTo("run_001");
        assertThat(root.path("query").path("message").asText()).isEqualTo("请帮我切换主题");
        assertThat(root.path("system").path("model").asText()).isEqualTo("gpt-5.2");

        JsonNode assistantReasoning = root.path("messages").get(1);
        assertThat(assistantReasoning.path("role").asText()).isEqualTo("assistant");
        assertThat(assistantReasoning.has("reasoning_content")).isTrue();
        assertThat(assistantReasoning.has("content")).isFalse();
        assertThat(assistantReasoning.path("_reasoningId").asText()).startsWith("reasoning_");

        JsonNode assistantToolCall = root.path("messages").get(2);
        assertThat(assistantToolCall.path("role").asText()).isEqualTo("assistant");
        assertThat(assistantToolCall.path("tool_calls")).hasSize(1);
        assertThat(assistantToolCall.path("tool_calls").get(0).path("_actionId").asText()).startsWith("action_");
        assertThat(assistantToolCall.path("tool_calls").get(0).has("_toolId")).isFalse();

        JsonNode toolResult = root.path("messages").get(3);
        assertThat(toolResult.path("role").asText()).isEqualTo("tool");
        assertThat(toolResult.path("_actionId").asText()).startsWith("action_");

        List<Message> historyMessages = store.loadHistoryMessages(chatId);
        assertThat(historyMessages).hasSize(4);
        assertThat(historyMessages.get(0)).isInstanceOf(UserMessage.class);
        assertThat(historyMessages.get(1)).isInstanceOf(AssistantMessage.class);
        assertThat(historyMessages.get(2)).isInstanceOf(ToolResponseMessage.class);
        assertThat(historyMessages.get(3)).isInstanceOf(AssistantMessage.class);
    }

    @Test
    void shouldTrimToConfiguredWindowSizeAndSkipReasoningInHistory() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(2);

        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);

        String chatId = "123e4567-e89b-12d3-a456-426614174001";
        store.appendRun(
                chatId,
                "run_001",
                query("run_001", chatId, "u1"),
                systemSnapshot("gpt-5.2", "system", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u1", 1000L),
                        ChatWindowMemoryStore.RunMessage.assistantReasoning("r1", 1001L, 1L, null),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a1", 1002L, 2L, null)
                )
        );
        store.appendRun(
                chatId,
                "run_002",
                query("run_002", chatId, "u2"),
                systemSnapshot("gpt-5.2", "system", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u2", 2000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a2", 2001L, 2L, null)
                )
        );
        store.appendRun(
                chatId,
                "run_003",
                query("run_003", chatId, "u3"),
                systemSnapshot("gpt-5.2", "system", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("u3", 3000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("a3", 3001L, 2L, null)
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

    @Test
    void shouldPersistSystemOnlyOnFirstRunOrWhenChanged() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(20);

        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);

        String chatId = "123e4567-e89b-12d3-a456-426614174002";
        store.appendRun(
                chatId,
                "run_001",
                query("run_001", chatId, "第一轮"),
                systemSnapshot("gpt-5.2", "你是一个有用的助手", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("第一轮", 1000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("ok", 1001L, 1L, null)
                )
        );
        store.appendRun(
                chatId,
                "run_002",
                query("run_002", chatId, "第二轮"),
                systemSnapshot("gpt-5.2", "你是一个有用的助手", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("第二轮", 2000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("ok", 2001L, 1L, null)
                )
        );
        store.appendRun(
                chatId,
                "run_003",
                query("run_003", chatId, "第三轮"),
                systemSnapshot("gpt-5.2", "你是另一个系统提示", true),
                null,
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("第三轮", 3000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("ok", 3001L, 1L, null)
                )
        );

        Path file = tempDir.resolve("chats").resolve(chatId + ".json");
        List<String> lines = Files.readAllLines(file).stream().filter(line -> !line.isBlank()).toList();
        assertThat(lines).hasSize(3);

        JsonNode first = objectMapper.readTree(lines.get(0));
        JsonNode second = objectMapper.readTree(lines.get(1));
        JsonNode third = objectMapper.readTree(lines.get(2));

        assertThat(first.has("system")).isTrue();
        assertThat(second.has("system")).isFalse();
        assertThat(third.has("system")).isTrue();
        assertThat(third.path("system").path("messages").get(0).path("content").asText()).isEqualTo("你是另一个系统提示");
    }

    @Test
    void shouldPersistSinglePlanSnapshotPerRunAndLoadLatest() throws Exception {
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(tempDir.resolve("chats").toString());
        properties.setK(20);

        ChatWindowMemoryStore store = new ChatWindowMemoryStore(objectMapper, properties);
        String chatId = "123e4567-e89b-12d3-a456-426614174003";

        store.appendRun(
                chatId,
                "run_001",
                query("run_001", chatId, "第一轮"),
                systemSnapshot("qwen3-max", "system", true),
                planSnapshot("plan_chat_001", List.of(
                        task("task0", "收集信息", "init"),
                        task("task1", "执行任务", "init")
                )),
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("第一轮", 1000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("ok", 1001L, 1L, null)
                )
        );

        store.appendRun(
                chatId,
                "run_002",
                query("run_002", chatId, "第二轮"),
                systemSnapshot("qwen3-max", "system", true),
                planSnapshot("plan_chat_001", List.of(
                        task("task0", "收集信息", "completed"),
                        task("task1", "执行任务", "in_progress")
                )),
                List.of(
                        ChatWindowMemoryStore.RunMessage.user("第二轮", 2000L),
                        ChatWindowMemoryStore.RunMessage.assistantContent("ok", 2001L, 1L, null)
                )
        );

        Path file = tempDir.resolve("chats").resolve(chatId + ".json");
        List<String> lines = Files.readAllLines(file).stream().filter(line -> !line.isBlank()).toList();
        assertThat(lines).hasSize(2);
        assertThat(lines.get(0)).contains("\"planSnapshot\":");
        assertThat(lines.get(1)).contains("\"planSnapshot\":");

        ChatWindowMemoryStore.PlanSnapshot latest = store.loadLatestPlanSnapshot(chatId);
        assertThat(latest).isNotNull();
        assertThat(latest.planId).isEqualTo("plan_chat_001");
        assertThat(latest.plan).hasSize(2);
        assertThat(latest.plan.get(0).status).isEqualTo("completed");
        assertThat(latest.plan.get(1).status).isEqualTo("init");
    }

    private Map<String, Object> query(String requestId, String chatId, String message) {
        Map<String, Object> query = new LinkedHashMap<>();
        query.put("requestId", requestId);
        query.put("chatId", chatId);
        query.put("role", "user");
        query.put("message", message);
        query.put("references", List.of());
        query.put("params", Map.of("x", 1));
        query.put("scene", Map.of("url", "https://example.com", "title", "demo"));
        query.put("stream", true);
        return query;
    }

    private ChatWindowMemoryStore.SystemSnapshot systemSnapshot(String model, String prompt, boolean stream) {
        ChatWindowMemoryStore.SystemSnapshot snapshot = new ChatWindowMemoryStore.SystemSnapshot();
        snapshot.model = model;
        snapshot.stream = stream;

        ChatWindowMemoryStore.SystemMessageSnapshot systemMessage = new ChatWindowMemoryStore.SystemMessageSnapshot();
        systemMessage.role = "system";
        systemMessage.content = prompt;
        snapshot.messages = List.of(systemMessage);

        ChatWindowMemoryStore.SystemToolSnapshot tool = new ChatWindowMemoryStore.SystemToolSnapshot();
        tool.type = "function";
        tool.name = "mock_sensitive_data_detector";
        tool.description = "检测敏感信息";
        tool.parameters = Map.of("type", "object", "properties", Map.of(), "additionalProperties", true);
        snapshot.tools = List.of(tool);
        return snapshot;
    }

    private ChatWindowMemoryStore.PlanSnapshot planSnapshot(
            String planId,
            List<ChatWindowMemoryStore.PlanTaskSnapshot> tasks
    ) {
        ChatWindowMemoryStore.PlanSnapshot snapshot = new ChatWindowMemoryStore.PlanSnapshot();
        snapshot.planId = planId;
        snapshot.plan = tasks;
        return snapshot;
    }

    private ChatWindowMemoryStore.PlanTaskSnapshot task(String taskId, String description, String status) {
        ChatWindowMemoryStore.PlanTaskSnapshot task = new ChatWindowMemoryStore.PlanTaskSnapshot();
        task.taskId = taskId;
        task.description = description;
        task.status = status;
        return task;
    }
}
