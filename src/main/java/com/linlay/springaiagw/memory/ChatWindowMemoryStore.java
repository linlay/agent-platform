package com.linlay.springaiagw.memory;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.ai.chat.messages.UserMessage;
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
import java.util.List;
import java.util.Map;
import java.util.UUID;

@Service
public class ChatWindowMemoryStore {

    private static final Logger log = LoggerFactory.getLogger(ChatWindowMemoryStore.class);
    private static final int FORMAT_VERSION = 1;

    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryProperties properties;

    public ChatWindowMemoryStore(ObjectMapper objectMapper, ChatWindowMemoryProperties properties) {
        this.objectMapper = objectMapper;
        this.properties = properties;
    }

    public List<Message> loadHistoryMessages(String chatId) {
        if (!isValidChatId(chatId)) {
            return List.of();
        }

        List<RunRecord> runs = readRecentRuns(chatId, normalizedWindowSize());
        List<Message> messages = new ArrayList<>();
        for (RunRecord run : runs) {
            if (run.messages == null) {
                continue;
            }
            for (StoredMessage stored : run.messages) {
                Message converted = toSpringMessage(stored);
                if (converted != null) {
                    messages.add(converted);
                }
            }
        }
        return List.copyOf(messages);
    }

    public void appendRun(String chatId, String runId, List<RunMessage> runMessages) {
        if (!isValidChatId(chatId) || runMessages == null || runMessages.isEmpty()) {
            return;
        }

        long now = System.currentTimeMillis();
        long ts = now;
        List<StoredMessage> storedMessages = new ArrayList<>();
        for (RunMessage message : runMessages) {
            if (message == null || !StringUtils.hasText(message.role())) {
                continue;
            }
            String role = message.role().trim().toLowerCase();
            if (!isSupportedRole(role)) {
                continue;
            }

            Long providedTs = message.ts();
            long messageTs = providedTs == null ? ts++ : providedTs;
            storedMessages.add(new StoredMessage(
                    role,
                    defaultString(message.content()),
                    messageTs,
                    nullable(message.name()),
                    nullable(message.toolCallId()),
                    parseJsonOrTextOrNull(message.toolArgs()),
                    nullable(message.toolResult())
            ));
        }
        if (storedMessages.isEmpty()) {
            return;
        }

        RunRecord run = new RunRecord();
        run.v = FORMAT_VERSION;
        run.chatId = chatId;
        run.runId = normalizeRunId(runId);
        run.updatedAt = now;
        run.messages = storedMessages;

        appendRunLine(chatId, run);
        trimToWindow(chatId, normalizedWindowSize());
    }

    private List<RunRecord> readRecentRuns(String chatId, int limit) {
        List<RunRecord> allRuns = readAllRuns(chatId);
        if (allRuns.size() <= limit) {
            return allRuns;
        }
        return List.copyOf(allRuns.subList(allRuns.size() - limit, allRuns.size()));
    }

    private List<RunRecord> readAllRuns(String chatId) {
        Path path = resolvePath(chatId);
        if (!Files.exists(path)) {
            return List.of();
        }

        try {
            String content = Files.readString(path, resolveCharset());
            if (!StringUtils.hasText(content)) {
                return List.of();
            }

            List<RunRecord> runs = new ArrayList<>();
            String[] lines = content.split("\\R");
            int lineIndex = 0;
            for (String line : lines) {
                if (!StringUtils.hasText(line)) {
                    continue;
                }
                RunRecord parsed = parseRunLine(line, chatId, lineIndex++);
                if (parsed != null) {
                    runs.add(parsed);
                }
            }
            if (!runs.isEmpty()) {
                return List.copyOf(runs);
            }

            RunRecord legacy = parseLegacyState(content, chatId);
            if (legacy == null) {
                return List.of();
            }
            return List.of(legacy);
        } catch (Exception ex) {
            log.warn("Cannot read memory file for chatId={}, fallback to empty history", chatId, ex);
            return List.of();
        }
    }

    private RunRecord parseRunLine(String line, String chatId, int index) {
        try {
            JsonNode node = objectMapper.readTree(line);
            if (node == null || !node.isObject()) {
                return null;
            }
            normalizeStoredMessages((ObjectNode) node);
            RunRecord run = objectMapper.treeToValue(node, RunRecord.class);
            if (run == null) {
                return null;
            }
            run.v = FORMAT_VERSION;
            run.chatId = StringUtils.hasText(run.chatId) ? run.chatId : chatId;
            run.runId = StringUtils.hasText(run.runId) ? run.runId : "legacy-" + index;
            if (run.messages == null) {
                run.messages = new ArrayList<>();
            } else {
                run.messages = new ArrayList<>(run.messages);
            }
            return run;
        } catch (Exception ignored) {
            return null;
        }
    }

    private RunRecord parseLegacyState(String content, String chatId) {
        try {
            JsonNode node = objectMapper.readTree(content);
            if (node == null || !node.isObject()) {
                return null;
            }
            normalizeStoredMessages((ObjectNode) node);
            LegacyChatState legacy = objectMapper.treeToValue(node, LegacyChatState.class);
            if (legacy == null || legacy.messages == null || legacy.messages.isEmpty()) {
                return null;
            }
            RunRecord run = new RunRecord();
            run.v = FORMAT_VERSION;
            run.chatId = StringUtils.hasText(legacy.chatId) ? legacy.chatId : chatId;
            long updatedAt = legacy.updatedAt > 0 ? legacy.updatedAt : System.currentTimeMillis();
            run.runId = "legacy-" + updatedAt;
            run.updatedAt = updatedAt;
            run.messages = new ArrayList<>(legacy.messages);
            return run;
        } catch (Exception ignored) {
            return null;
        }
    }

    private void appendRunLine(String chatId, RunRecord run) {
        Path path = resolvePath(chatId);
        try {
            Files.createDirectories(path.getParent());
            String jsonLine = objectMapper.writeValueAsString(run) + System.lineSeparator();
            Files.writeString(
                    path,
                    jsonLine,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.APPEND,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot append run memory for chatId=" + chatId, ex);
        }
    }

    private void trimToWindow(String chatId, int windowSize) {
        Path path = resolvePath(chatId);
        if (!Files.exists(path)) {
            return;
        }

        try {
            List<String> lines = Files.readAllLines(path, resolveCharset()).stream()
                    .filter(StringUtils::hasText)
                    .toList();
            if (lines.size() <= windowSize) {
                return;
            }
            List<String> latest = lines.subList(lines.size() - windowSize, lines.size());
            String content = String.join(System.lineSeparator(), latest) + System.lineSeparator();
            Files.writeString(
                    path,
                    content,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.TRUNCATE_EXISTING,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot trim run memory for chatId=" + chatId, ex);
        }
    }

    private Message toSpringMessage(StoredMessage message) {
        if (message == null || !StringUtils.hasText(message.role)) {
            return null;
        }

        String role = message.role.trim().toLowerCase();
        return switch (role) {
            case "user" -> StringUtils.hasText(message.content) ? new UserMessage(message.content) : null;
            case "assistant" -> toAssistantMessage(message);
            case "tool" -> toToolMessage(message);
            case "system" -> StringUtils.hasText(message.content) ? new SystemMessage(message.content) : null;
            default -> null;
        };
    }

    private Message toAssistantMessage(StoredMessage message) {
        boolean hasToolCall = StringUtils.hasText(message.toolCallId)
                && StringUtils.hasText(message.name)
                && message.toolArgs != null;
        if (!hasToolCall) {
            return StringUtils.hasText(message.content) ? new AssistantMessage(message.content) : null;
        }

        AssistantMessage.ToolCall toolCall = new AssistantMessage.ToolCall(
                message.toolCallId,
                "function",
                message.name,
                stringifyNode(message.toolArgs)
        );
        String content = StringUtils.hasText(message.content) ? message.content : "";
        return new AssistantMessage(content, Map.of(), List.of(toolCall));
    }

    private Message toToolMessage(StoredMessage message) {
        if (!StringUtils.hasText(message.toolCallId) || !StringUtils.hasText(message.name)) {
            return null;
        }
        String responseData = StringUtils.hasText(message.content)
                ? message.content
                : defaultString(message.toolResult);
        ToolResponseMessage.ToolResponse toolResponse = new ToolResponseMessage.ToolResponse(
                message.toolCallId,
                message.name,
                responseData
        );
        return new ToolResponseMessage(List.of(toolResponse));
    }

    private void normalizeStoredMessages(ObjectNode root) {
        JsonNode messages = root.path("messages");
        if (!messages.isArray()) {
            return;
        }
        for (JsonNode message : messages) {
            if (!(message instanceof ObjectNode messageNode)) {
                continue;
            }
            JsonNode toolResult = messageNode.get("toolResult");
            if (toolResult == null || toolResult.isNull()) {
                continue;
            }
            messageNode.put("toolResult", stringifyNode(toolResult));
        }
    }

    private JsonNode parseJsonOrTextOrNull(String raw) {
        if (!StringUtils.hasText(raw)) {
            return null;
        }
        String normalized = raw.trim();
        try {
            return objectMapper.readTree(normalized);
        } catch (JsonProcessingException ex) {
            ObjectNode node = objectMapper.createObjectNode();
            node.put("text", normalized);
            return node;
        }
    }

    private String stringifyNode(JsonNode node) {
        if (node == null || node.isNull()) {
            return "null";
        }
        if (node.isTextual()) {
            return node.asText();
        }
        try {
            return objectMapper.writeValueAsString(node);
        } catch (JsonProcessingException ex) {
            return String.valueOf(node);
        }
    }

    private String normalizeRunId(String runId) {
        if (StringUtils.hasText(runId)) {
            return runId.trim();
        }
        return UUID.randomUUID().toString();
    }

    private String defaultString(String value) {
        return value == null ? "" : value;
    }

    private String nullable(String value) {
        return StringUtils.hasText(value) ? value.trim() : null;
    }

    private boolean isSupportedRole(String role) {
        return "system".equals(role)
                || "user".equals(role)
                || "assistant".equals(role)
                || "tool".equals(role);
    }

    private int normalizedWindowSize() {
        return Math.max(1, properties.getK());
    }

    private Path resolvePath(String chatId) {
        Path dir = Paths.get(properties.getDir()).toAbsolutePath().normalize();
        return dir.resolve(chatId + ".json");
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

    public record RunMessage(
            String role,
            String content,
            String name,
            String toolCallId,
            String toolArgs,
            String toolResult,
            Long ts
    ) {
        public static RunMessage user(String content) {
            return new RunMessage("user", content, null, null, null, null, null);
        }

        public static RunMessage assistantContent(String content) {
            return new RunMessage("assistant", content, null, null, null, null, null);
        }

        public static RunMessage assistantToolCall(String toolName, String toolCallId, String toolArgs) {
            return new RunMessage("assistant", "", toolName, toolCallId, toolArgs, null, null);
        }

        public static RunMessage toolResult(String toolName, String toolCallId, String toolArgs, String toolResult) {
            return new RunMessage("tool", toolResult, toolName, toolCallId, toolArgs, toolResult, null);
        }
    }

    public static class RunRecord {
        public int v = FORMAT_VERSION;
        public String chatId;
        public String runId;
        public long updatedAt;
        public List<StoredMessage> messages = new ArrayList<>();
    }

    public static class StoredMessage {
        public String role;
        public String content;
        public long ts;
        public String name;
        public String toolCallId;
        public JsonNode toolArgs;
        public String toolResult;

        public StoredMessage() {
        }

        public StoredMessage(
                String role,
                String content,
                long ts,
                String name,
                String toolCallId,
                JsonNode toolArgs,
                String toolResult
        ) {
            this.role = role;
            this.content = content;
            this.ts = ts;
            this.name = name;
            this.toolCallId = toolCallId;
            this.toolArgs = toolArgs;
            this.toolResult = toolResult;
        }
    }

    public static class LegacyChatState {
        public String chatId;
        public long updatedAt;
        public List<StoredMessage> messages;
    }
}
