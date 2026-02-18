package com.linlay.springaiagw.memory;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
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
import java.util.Comparator;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

@Service
public class ChatWindowMemoryStore {

    private static final Logger log = LoggerFactory.getLogger(ChatWindowMemoryStore.class);

    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryProperties properties;

    public ChatWindowMemoryStore(ObjectMapper objectMapper, ChatWindowMemoryProperties properties) {
        this.objectMapper = objectMapper;
        this.properties = properties;
    }

    // ========= Public API =========

    public List<Message> loadHistoryMessages(String chatId) {
        if (!isValidChatId(chatId)) {
            return List.of();
        }

        int windowSize = normalizedWindowSize();
        List<ParsedLine> allLines = readAllParsedLines(chatId);

        LinkedHashMap<String, List<ParsedStepLine>> stepsByRunId = new LinkedHashMap<>();
        for (ParsedLine line : allLines) {
            if (line instanceof ParsedStepLine step) {
                stepsByRunId.computeIfAbsent(step.runId(), k -> new ArrayList<>()).add(step);
            }
        }

        List<String> runIds = new ArrayList<>(stepsByRunId.keySet());
        int fromIndex = Math.max(0, runIds.size() - windowSize);
        List<String> recentRunIds = runIds.subList(fromIndex, runIds.size());

        List<Message> messages = new ArrayList<>();
        for (String runId : recentRunIds) {
            List<ParsedStepLine> steps = stepsByRunId.get(runId);
            steps.sort(Comparator.comparingInt(ParsedStepLine::seq));
            for (ParsedStepLine step : steps) {
                if (step.messages() == null) {
                    continue;
                }
                for (StoredMessage stored : step.messages()) {
                    Message converted = toSpringMessage(stored);
                    if (converted != null) {
                        messages.add(converted);
                    }
                }
            }
        }
        return List.copyOf(messages);
    }

    public void appendQueryLine(String chatId, String runId, Map<String, Object> query) {
        if (!isValidChatId(chatId)) {
            return;
        }

        long now = System.currentTimeMillis();
        QueryLine line = new QueryLine();
        line.chatId = chatId;
        line.runId = normalizeRunId(runId);
        line.updatedAt = now;
        line.query = normalizeQuery(query);

        appendLine(chatId, line);
    }

    public void appendStepLine(
            String chatId,
            String runId,
            String stage,
            int seq,
            String taskId,
            SystemSnapshot system,
            PlanSnapshot planSnapshot,
            List<RunMessage> runMessages
    ) {
        if (!isValidChatId(chatId) || runMessages == null || runMessages.isEmpty()) {
            return;
        }

        long now = System.currentTimeMillis();
        String normalizedRunId = normalizeRunId(runId);
        List<StoredMessage> storedMessages = convertRunMessages(normalizedRunId, runMessages);
        if (storedMessages.isEmpty()) {
            return;
        }

        StepLine line = new StepLine();
        line.chatId = chatId;
        line.runId = normalizedRunId;
        line.stage = hasText(stage) ? stage.trim() : "oneshot";
        line.seq = seq;
        line.taskId = hasText(taskId) ? taskId.trim() : null;
        line.updatedAt = now;
        line.system = normalizeSystemSnapshot(system);
        line.planSnapshot = normalizePlanSnapshot(planSnapshot);
        line.messages = storedMessages;

        appendLine(chatId, line);
    }

    public PlanSnapshot loadLatestPlanSnapshot(String chatId) {
        if (!isValidChatId(chatId)) {
            return null;
        }
        List<ParsedLine> lines = readAllParsedLines(chatId);
        for (int i = lines.size() - 1; i >= 0; i--) {
            if (lines.get(i) instanceof ParsedStepLine step && step.planSnapshot() != null) {
                PlanSnapshot normalized = normalizePlanSnapshot(step.planSnapshot());
                if (normalized != null && normalized.plan != null && !normalized.plan.isEmpty()) {
                    return normalized;
                }
            }
        }
        return null;
    }

    public SystemSnapshot loadLatestSystemSnapshot(String chatId) {
        if (!isValidChatId(chatId)) {
            return null;
        }
        List<ParsedLine> lines = readAllParsedLines(chatId);
        for (int i = lines.size() - 1; i >= 0; i--) {
            if (lines.get(i) instanceof ParsedStepLine step && step.system() != null) {
                return step.system();
            }
        }
        return null;
    }

    public void trimToWindow(String chatId) {
        int windowSize = normalizedWindowSize();
        Path path = resolvePath(chatId);
        if (!Files.exists(path)) {
            return;
        }

        try {
            List<String> rawLines = Files.readAllLines(path, resolveCharset()).stream()
                    .filter(StringUtils::hasText)
                    .toList();
            if (rawLines.isEmpty()) {
                return;
            }

            LinkedHashMap<String, List<Integer>> lineIndexesByRunId = new LinkedHashMap<>();
            for (int i = 0; i < rawLines.size(); i++) {
                String runId = extractRunId(rawLines.get(i));
                if (runId != null) {
                    lineIndexesByRunId.computeIfAbsent(runId, k -> new ArrayList<>()).add(i);
                }
            }

            if (lineIndexesByRunId.size() <= windowSize) {
                return;
            }

            List<String> runIds = new ArrayList<>(lineIndexesByRunId.keySet());
            int keepFrom = runIds.size() - windowSize;
            Set<Integer> dropIndexes = new HashSet<>();
            for (int i = 0; i < keepFrom; i++) {
                dropIndexes.addAll(lineIndexesByRunId.get(runIds.get(i)));
            }

            List<String> retained = new ArrayList<>(rawLines.size() - dropIndexes.size());
            for (int i = 0; i < rawLines.size(); i++) {
                if (!dropIndexes.contains(i)) {
                    retained.add(rawLines.get(i));
                }
            }

            String content = String.join(System.lineSeparator(), retained) + System.lineSeparator();
            Files.writeString(
                    path,
                    content,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.TRUNCATE_EXISTING,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot trim memory for chatId=" + chatId, ex);
        }
    }

    public boolean isSameSystem(SystemSnapshot left, SystemSnapshot right) {
        if (left == null || right == null) {
            return false;
        }
        JsonNode leftNode = objectMapper.valueToTree(left);
        JsonNode rightNode = objectMapper.valueToTree(right);
        return leftNode.equals(rightNode);
    }

    // ========= RunMessage -> StoredMessage conversion =========

    private List<StoredMessage> convertRunMessages(String runId, List<RunMessage> runMessages) {
        Map<String, ToolIdentity> toolIdentityByCallId = new LinkedHashMap<>();
        List<StoredMessage> storedMessages = new ArrayList<>();
        int reasoningIndex = 0;
        int contentIndex = 0;
        long tsCursor = System.currentTimeMillis();
        for (RunMessage message : runMessages) {
            if (message == null || !StringUtils.hasText(message.kind())) {
                continue;
            }
            long ts = message.ts() == null ? tsCursor++ : message.ts();
            StoredMessage converted = switch (message.kind().trim().toLowerCase()) {
                case "user_content" -> toUserStoredMessage(message, ts);
                case "assistant_reasoning" -> toAssistantReasoningMessage(runId, message, ts, reasoningIndex++);
                case "assistant_content" -> toAssistantContentMessage(runId, message, ts, contentIndex++);
                case "assistant_tool_call" -> toAssistantToolCallMessage(message, ts, toolIdentityByCallId);
                case "tool_result" -> toToolResultMessage(message, ts, toolIdentityByCallId);
                default -> null;
            };
            if (converted != null) {
                storedMessages.add(converted);
            }
        }
        return storedMessages;
    }

    private StoredMessage toUserStoredMessage(RunMessage message, long ts) {
        if (!StringUtils.hasText(message.text())) {
            return null;
        }
        StoredMessage stored = new StoredMessage();
        stored.role = "user";
        stored.content = textContent(message.text());
        stored.ts = ts;
        return stored;
    }

    private StoredMessage toAssistantReasoningMessage(String runId, RunMessage message, long ts, int index) {
        if (!StringUtils.hasText(message.text())) {
            return null;
        }
        StoredMessage stored = new StoredMessage();
        stored.role = "assistant";
        stored.reasoningContent = textContent(message.text());
        stored.ts = ts;
        stored.reasoningId = hasText(message.reasoningId())
                ? message.reasoningId().trim()
                : shortId("reasoning", runId + "_" + ts + "_" + index);
        stored.timing = positiveOrNull(message.timing());
        stored.usage = usageOrDefault(message.usage());
        return stored;
    }

    private StoredMessage toAssistantContentMessage(String runId, RunMessage message, long ts, int index) {
        if (!StringUtils.hasText(message.text())) {
            return null;
        }
        StoredMessage stored = new StoredMessage();
        stored.role = "assistant";
        stored.content = textContent(message.text());
        stored.ts = ts;
        stored.contentId = hasText(message.contentId())
                ? message.contentId().trim()
                : shortId("content", runId + "_" + ts + "_" + index);
        stored.timing = positiveOrNull(message.timing());
        stored.usage = usageOrDefault(message.usage());
        return stored;
    }

    private StoredMessage toAssistantToolCallMessage(
            RunMessage message,
            long ts,
            Map<String, ToolIdentity> toolIdentityByCallId
    ) {
        if (!hasText(message.name()) || !hasText(message.toolCallId()) || !hasText(message.toolArgs())) {
            return null;
        }
        String toolCallId = message.toolCallId().trim();
        String toolName = message.name().trim();

        ToolIdentity identity = toolIdentityByCallId.computeIfAbsent(
                toolCallId,
                key -> createToolIdentity(toolCallId, toolName, message.toolCallType())
        );

        StoredToolCall toolCall = new StoredToolCall();
        toolCall.id = toolCallId;
        toolCall.type = hasText(message.toolCallType()) ? message.toolCallType().trim() : "function";
        FunctionCall function = new FunctionCall();
        function.name = toolName;
        function.arguments = message.toolArgs().trim();
        toolCall.function = function;
        if (identity.action) {
            toolCall.actionId = identity.id;
        } else {
            toolCall.toolId = identity.id;
        }

        StoredMessage stored = new StoredMessage();
        stored.role = "assistant";
        stored.toolCalls = List.of(toolCall);
        stored.ts = ts;
        stored.timing = positiveOrNull(message.timing());
        stored.usage = usageOrDefault(message.usage());
        return stored;
    }

    private StoredMessage toToolResultMessage(
            RunMessage message,
            long ts,
            Map<String, ToolIdentity> toolIdentityByCallId
    ) {
        if (!hasText(message.name()) || !hasText(message.toolCallId())) {
            return null;
        }
        String toolCallId = message.toolCallId().trim();
        String toolName = message.name().trim();

        ToolIdentity identity = toolIdentityByCallId.computeIfAbsent(
                toolCallId,
                key -> createToolIdentity(toolCallId, toolName, null)
        );

        StoredMessage stored = new StoredMessage();
        stored.role = "tool";
        stored.name = toolName;
        stored.toolCallId = toolCallId;
        stored.content = textContent(defaultString(message.text()));
        stored.ts = ts;
        stored.timing = positiveOrNull(message.timing());
        if (identity.action) {
            stored.actionId = identity.id;
        } else {
            stored.toolId = identity.id;
        }
        return stored;
    }

    // ========= Reading & Parsing =========

    private List<ParsedLine> readAllParsedLines(String chatId) {
        Path path = resolvePath(chatId);
        if (!Files.exists(path)) {
            return List.of();
        }

        try {
            List<ParsedLine> lines = new ArrayList<>();
            for (String rawLine : Files.readAllLines(path, resolveCharset())) {
                if (!StringUtils.hasText(rawLine)) {
                    continue;
                }
                ParsedLine parsed = parseParsedLine(rawLine);
                if (parsed != null) {
                    lines.add(parsed);
                }
            }
            return List.copyOf(lines);
        } catch (Exception ex) {
            log.warn("Cannot read memory file for chatId={}, fallback to empty history", chatId, ex);
            return List.of();
        }
    }

    private ParsedLine parseParsedLine(String rawLine) {
        try {
            JsonNode node = objectMapper.readTree(rawLine);
            if (node == null || !node.isObject()) {
                return null;
            }
            String type = node.path("_type").asText("");
            return switch (type) {
                case "query" -> parseQueryLineNode(node);
                case "step" -> parseStepLineNode(node);
                default -> null;
            };
        } catch (Exception ignored) {
            return null;
        }
    }

    private ParsedQueryLine parseQueryLineNode(JsonNode node) {
        String chatId = node.path("chatId").asText(null);
        String runId = node.path("runId").asText(null);
        long updatedAt = node.path("updatedAt").asLong(0);
        Map<String, Object> query;
        if (node.has("query") && node.get("query").isObject()) {
            query = objectMapper.convertValue(
                    node.get("query"),
                    objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class)
            );
        } else {
            query = new LinkedHashMap<>();
        }
        return new ParsedQueryLine(chatId, runId, updatedAt, query);
    }

    private ParsedStepLine parseStepLineNode(JsonNode node) {
        try {
            String chatId = node.path("chatId").asText(null);
            String runId = node.path("runId").asText(null);
            String stage = node.path("_stage").asText(null);
            int seq = node.path("_seq").asInt(0);
            String taskId = node.has("taskId") && !node.get("taskId").isNull()
                    ? node.path("taskId").asText(null)
                    : null;
            long updatedAt = node.path("updatedAt").asLong(0);

            SystemSnapshot system = null;
            if (node.has("system") && !node.get("system").isNull()) {
                system = objectMapper.treeToValue(node.get("system"), SystemSnapshot.class);
            }

            PlanSnapshot planSnapshot = null;
            if (node.has("planSnapshot") && !node.get("planSnapshot").isNull()) {
                planSnapshot = objectMapper.treeToValue(node.get("planSnapshot"), PlanSnapshot.class);
            }

            List<StoredMessage> messages = new ArrayList<>();
            if (node.has("messages") && node.get("messages").isArray()) {
                for (JsonNode msgNode : node.get("messages")) {
                    StoredMessage msg = objectMapper.treeToValue(msgNode, StoredMessage.class);
                    if (msg != null) {
                        messages.add(msg);
                    }
                }
            }

            return new ParsedStepLine(chatId, runId, stage, seq, taskId, updatedAt, system, planSnapshot, List.copyOf(messages));
        } catch (Exception ignored) {
            return null;
        }
    }

    private String extractRunId(String rawLine) {
        try {
            JsonNode node = objectMapper.readTree(rawLine);
            if (node != null && node.has("runId")) {
                String runId = node.path("runId").asText(null);
                return hasText(runId) ? runId : null;
            }
        } catch (Exception ignored) {
        }
        return null;
    }

    // ========= Writing =========

    private void appendLine(String chatId, Object line) {
        Path path = resolvePath(chatId);
        try {
            Files.createDirectories(path.getParent());
            String jsonLine = objectMapper.writeValueAsString(line) + System.lineSeparator();
            Files.writeString(
                    path,
                    jsonLine,
                    resolveCharset(),
                    StandardOpenOption.CREATE,
                    StandardOpenOption.APPEND,
                    StandardOpenOption.WRITE
            );
        } catch (IOException ex) {
            throw new IllegalStateException("Cannot append memory line for chatId=" + chatId, ex);
        }
    }

    // ========= Spring Message Conversion =========

    private Message toSpringMessage(StoredMessage message) {
        if (message == null || !hasText(message.role)) {
            return null;
        }
        String role = message.role.trim().toLowerCase();
        return switch (role) {
            case "user" -> toUserMessage(message);
            case "assistant" -> toAssistantMessage(message);
            case "tool" -> toToolMessage(message);
            case "system" -> toSystemMessage(message);
            default -> null;
        };
    }

    private Message toUserMessage(StoredMessage message) {
        String text = textFromContentParts(message.content);
        return hasText(text) ? new UserMessage(text) : null;
    }

    private Message toSystemMessage(StoredMessage message) {
        String text = textFromContentParts(message.content);
        return hasText(text) ? new SystemMessage(text) : null;
    }

    private Message toAssistantMessage(StoredMessage message) {
        if (message.toolCalls != null && !message.toolCalls.isEmpty()) {
            List<AssistantMessage.ToolCall> toolCalls = new ArrayList<>();
            for (StoredToolCall toolCall : message.toolCalls) {
                if (toolCall == null || toolCall.function == null || !hasText(toolCall.id) || !hasText(toolCall.function.name)) {
                    continue;
                }
                String type = hasText(toolCall.type) ? toolCall.type : "function";
                String arguments = hasText(toolCall.function.arguments) ? toolCall.function.arguments : "{}";
                toolCalls.add(new AssistantMessage.ToolCall(
                        toolCall.id,
                        type,
                        toolCall.function.name,
                        arguments
                ));
            }
            if (!toolCalls.isEmpty()) {
                String content = textFromContentParts(message.content);
                return new AssistantMessage(defaultString(content), Map.of(), toolCalls);
            }
            return null;
        }

        // reasoning_content is intentionally excluded from next-round context.
        if (message.reasoningContent != null && !message.reasoningContent.isEmpty()) {
            return null;
        }

        String text = textFromContentParts(message.content);
        return hasText(text) ? new AssistantMessage(text) : null;
    }

    private Message toToolMessage(StoredMessage message) {
        if (!hasText(message.toolCallId) || !hasText(message.name)) {
            return null;
        }
        String responseData = textFromContentParts(message.content);
        ToolResponseMessage.ToolResponse toolResponse = new ToolResponseMessage.ToolResponse(
                message.toolCallId,
                message.name,
                defaultString(responseData)
        );
        return new ToolResponseMessage(List.of(toolResponse));
    }

    private String textFromContentParts(List<ContentPart> contentParts) {
        if (contentParts == null || contentParts.isEmpty()) {
            return "";
        }
        StringBuilder sb = new StringBuilder();
        for (ContentPart contentPart : contentParts) {
            if (contentPart == null || !hasText(contentPart.text)) {
                continue;
            }
            sb.append(contentPart.text);
        }
        return sb.toString();
    }

    private List<ContentPart> textContent(String text) {
        if (!hasText(text)) {
            return null;
        }
        ContentPart part = new ContentPart();
        part.type = "text";
        part.text = text;
        return List.of(part);
    }

    // ========= Normalization Helpers =========

    private Map<String, Object> normalizeQuery(Map<String, Object> query) {
        if (query == null) {
            return new LinkedHashMap<>();
        }
        return objectMapper.convertValue(query, objectMapper.getTypeFactory().constructMapType(LinkedHashMap.class, String.class, Object.class));
    }

    private SystemSnapshot normalizeSystemSnapshot(SystemSnapshot source) {
        if (source == null) {
            return null;
        }
        SystemSnapshot normalized = objectMapper.convertValue(source, SystemSnapshot.class);
        if (normalized == null) {
            return null;
        }
        normalized.model = nullable(normalized.model);
        normalized.messages = normalizeSystemMessages(normalized.messages);
        normalized.tools = normalizeSystemTools(normalized.tools);
        if (normalized.model == null
                && (normalized.messages == null || normalized.messages.isEmpty())
                && (normalized.tools == null || normalized.tools.isEmpty())
                && normalized.stream == null) {
            return null;
        }
        return normalized;
    }

    private PlanSnapshot normalizePlanSnapshot(PlanSnapshot source) {
        if (source == null) {
            return null;
        }
        PlanSnapshot normalized = objectMapper.convertValue(source, PlanSnapshot.class);
        if (normalized == null || !hasText(normalized.planId) || normalized.plan == null || normalized.plan.isEmpty()) {
            return null;
        }
        List<PlanTaskSnapshot> plan = new ArrayList<>();
        for (PlanTaskSnapshot task : normalized.plan) {
            if (task == null || !hasText(task.taskId) || !hasText(task.description)) {
                continue;
            }
            PlanTaskSnapshot item = new PlanTaskSnapshot();
            item.taskId = task.taskId.trim();
            item.description = task.description.trim();
            item.status = normalizeStatus(task.status);
            plan.add(item);
        }
        if (plan.isEmpty()) {
            return null;
        }
        PlanSnapshot snapshot = new PlanSnapshot();
        snapshot.planId = normalized.planId.trim();
        snapshot.plan = List.copyOf(plan);
        return snapshot;
    }

    private List<SystemMessageSnapshot> normalizeSystemMessages(List<SystemMessageSnapshot> messages) {
        if (messages == null || messages.isEmpty()) {
            return null;
        }
        List<SystemMessageSnapshot> normalized = new ArrayList<>();
        for (SystemMessageSnapshot message : messages) {
            if (message == null || !hasText(message.role) || !hasText(message.content)) {
                continue;
            }
            SystemMessageSnapshot item = new SystemMessageSnapshot();
            item.role = message.role.trim();
            item.content = message.content;
            normalized.add(item);
        }
        return normalized.isEmpty() ? null : List.copyOf(normalized);
    }

    private List<SystemToolSnapshot> normalizeSystemTools(List<SystemToolSnapshot> tools) {
        if (tools == null || tools.isEmpty()) {
            return null;
        }
        List<SystemToolSnapshot> normalized = new ArrayList<>();
        for (SystemToolSnapshot tool : tools) {
            if (tool == null || !hasText(tool.type) || !hasText(tool.name)) {
                continue;
            }
            SystemToolSnapshot item = new SystemToolSnapshot();
            item.type = tool.type.trim();
            item.name = tool.name.trim();
            item.description = nullable(tool.description);
            item.parameters = tool.parameters == null ? null : new LinkedHashMap<>(tool.parameters);
            normalized.add(item);
        }
        return normalized.isEmpty() ? null : List.copyOf(normalized);
    }

    // ========= Tool Identity =========

    private ToolIdentity createToolIdentity(String toolCallId, String toolName, String toolType) {
        boolean action = "action".equalsIgnoreCase(normalizeType(toolType)) || isActionTool(toolName);
        String prefix = action ? "action" : "tool";
        String id = shortId(prefix, toolCallId);
        return new ToolIdentity(id, action);
    }

    private String normalizeType(String rawType) {
        if (!hasText(rawType)) {
            return "";
        }
        return rawType.trim().toLowerCase();
    }

    private boolean isActionTool(String toolName) {
        if (!hasText(toolName)) {
            return false;
        }
        String normalized = toolName.trim().toLowerCase();
        List<String> actionTools = properties.getActionTools();
        if (actionTools == null || actionTools.isEmpty()) {
            return false;
        }
        for (String configured : actionTools) {
            if (hasText(configured) && normalized.equals(configured.trim().toLowerCase())) {
                return true;
            }
        }
        return false;
    }

    // ========= Utility =========

    private String shortId(String prefix, String seed) {
        String shortPart;
        if (hasText(seed)) {
            shortPart = UUID.nameUUIDFromBytes(seed.getBytes(StandardCharsets.UTF_8))
                    .toString()
                    .replace("-", "")
                    .substring(0, 8);
        } else {
            shortPart = UUID.randomUUID().toString().replace("-", "").substring(0, 8);
        }
        return prefix + "_" + shortPart;
    }

    private Map<String, Object> usageOrDefault(Map<String, Object> usage) {
        if (usage != null && !usage.isEmpty()) {
            return usage;
        }
        LinkedHashMap<String, Object> defaults = new LinkedHashMap<>();
        defaults.put("input_tokens", null);
        defaults.put("output_tokens", null);
        defaults.put("total_tokens", null);
        return defaults;
    }

    private Long positiveOrNull(Long value) {
        if (value == null || value <= 0) {
            return null;
        }
        return value;
    }

    private String normalizeRunId(String runId) {
        if (hasText(runId)) {
            return runId.trim();
        }
        return UUID.randomUUID().toString();
    }

    private String nullable(String value) {
        return hasText(value) ? value.trim() : null;
    }

    private String defaultString(String value) {
        return value == null ? "" : value;
    }

    private String normalizeStatus(String raw) {
        if (!hasText(raw)) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase();
        return switch (normalized) {
            case "in_progress" -> "init";
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
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

    private boolean hasText(String value) {
        return StringUtils.hasText(value);
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

    // ========= Inner Types =========

    public record RunMessage(
            String role,
            String kind,
            String text,
            String name,
            String toolCallId,
            String toolCallType,
            String toolArgs,
            String reasoningId,
            String contentId,
            Long ts,
            Long timing,
            Map<String, Object> usage
    ) {

        public static RunMessage user(String content) {
            return new RunMessage("user", "user_content", content, null, null, null, null, null, null, null, null, null);
        }

        public static RunMessage user(String content, Long ts) {
            return new RunMessage("user", "user_content", content, null, null, null, null, null, null, ts, null, null);
        }

        public static RunMessage assistantReasoning(String content, Long ts, Long timing, Map<String, Object> usage) {
            return new RunMessage("assistant", "assistant_reasoning", content, null, null, null, null, null, null, ts, timing, usage);
        }

        public static RunMessage assistantContent(String content) {
            return new RunMessage("assistant", "assistant_content", content, null, null, null, null, null, null, null, null, null);
        }

        public static RunMessage assistantContent(String content, Long ts, Long timing, Map<String, Object> usage) {
            return new RunMessage("assistant", "assistant_content", content, null, null, null, null, null, null, ts, timing, usage);
        }

        public static RunMessage assistantToolCall(String toolName, String toolCallId, String toolArgs) {
            return assistantToolCall(toolName, toolCallId, "function", toolArgs, null, null, null);
        }

        public static RunMessage assistantToolCall(
                String toolName,
                String toolCallId,
                String toolArgs,
                Long ts,
                Long timing,
                Map<String, Object> usage
        ) {
            return assistantToolCall(toolName, toolCallId, "function", toolArgs, ts, timing, usage);
        }

        public static RunMessage assistantToolCall(
                String toolName,
                String toolCallId,
                String toolCallType,
                String toolArgs,
                Long ts,
                Long timing,
                Map<String, Object> usage
        ) {
            return new RunMessage("assistant", "assistant_tool_call", null, toolName, toolCallId, toolCallType, toolArgs, null, null, ts, timing, usage);
        }

        public static RunMessage toolResult(String toolName, String toolCallId, String toolArgs, String toolResult) {
            return new RunMessage("tool", "tool_result", toolResult, toolName, toolCallId, null, toolArgs, null, null, null, null, null);
        }

        public static RunMessage toolResult(
                String toolName,
                String toolCallId,
                String toolResult,
                Long ts,
                Long timing
        ) {
            return new RunMessage("tool", "tool_result", toolResult, toolName, toolCallId, null, null, null, null, ts, timing, null);
        }
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class QueryLine {
        @JsonProperty("_type")
        public String type = "query";
        public String chatId;
        public String runId;
        public long updatedAt;
        public Map<String, Object> query = new LinkedHashMap<>();
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class StepLine {
        @JsonProperty("_type")
        public String type = "step";
        public String chatId;
        public String runId;
        @JsonProperty("_stage")
        public String stage;
        @JsonProperty("_seq")
        public int seq;
        public String taskId;
        public long updatedAt;
        public SystemSnapshot system;
        public PlanSnapshot planSnapshot;
        public List<StoredMessage> messages = new ArrayList<>();
    }

    private sealed interface ParsedLine permits ParsedQueryLine, ParsedStepLine {
        String runId();
    }

    private record ParsedQueryLine(
            String chatId,
            String runId,
            long updatedAt,
            Map<String, Object> query
    ) implements ParsedLine {
    }

    private record ParsedStepLine(
            String chatId,
            String runId,
            String stage,
            int seq,
            String taskId,
            long updatedAt,
            SystemSnapshot system,
            PlanSnapshot planSnapshot,
            List<StoredMessage> messages
    ) implements ParsedLine {
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class SystemSnapshot {
        public String model;
        public List<SystemMessageSnapshot> messages;
        public List<SystemToolSnapshot> tools;
        public Boolean stream;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class SystemMessageSnapshot {
        public String role;
        public String content;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class SystemToolSnapshot {
        public String type;
        public String name;
        public String description;
        public Map<String, Object> parameters;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class PlanSnapshot {
        public String planId;
        public List<PlanTaskSnapshot> plan;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class PlanTaskSnapshot {
        public String taskId;
        public String description;
        public String status;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class StoredMessage {
        public String role;
        public List<ContentPart> content;
        @JsonProperty("reasoning_content")
        public List<ContentPart> reasoningContent;
        @JsonProperty("tool_calls")
        public List<StoredToolCall> toolCalls;
        public Long ts;
        public String name;
        @JsonProperty("tool_call_id")
        public String toolCallId;

        @JsonProperty("_reasoningId")
        public String reasoningId;
        @JsonProperty("_contentId")
        public String contentId;
        @JsonProperty("_toolId")
        public String toolId;
        @JsonProperty("_actionId")
        public String actionId;
        @JsonProperty("_timing")
        public Long timing;
        @JsonProperty("_usage")
        public Map<String, Object> usage;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class ContentPart {
        public String type;
        public String text;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class StoredToolCall {
        public String id;
        public String type;
        public FunctionCall function;
        @JsonProperty("_toolId")
        public String toolId;
        @JsonProperty("_actionId")
        public String actionId;
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class FunctionCall {
        public String name;
        public String arguments;
    }

    private record ToolIdentity(String id, boolean action) {
    }
}
