package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.agent.AgentCatalogProperties;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Pattern;

@Component
public class AgentFileCreateTool extends AbstractDeterministicTool {

    private static final Pattern AGENT_ID_PATTERN = Pattern.compile("^[A-Za-z0-9_-]{1,64}$");
    private static final String DEFAULT_DESCRIPTION = "由 agentCreator 创建的智能体";
    private static final String DEFAULT_MODEL = "qwen3-max";
    private static final String DEFAULT_PROVIDER_KEY = "bailian";
    private static final String DEFAULT_PROMPT = "你是通用助理，回答要清晰和可执行。";

    private final Path agentsDir;

    @Autowired
    public AgentFileCreateTool(AgentCatalogProperties properties) {
        this(Path.of(properties.getExternalDir()));
    }

    public AgentFileCreateTool(Path agentsDir) {
        this.agentsDir = agentsDir.toAbsolutePath().normalize();
    }

    @Override
    public String name() {
        return "agent_file_create";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        ObjectNode result = OBJECT_MAPPER.createObjectNode();
        result.put("tool", name());
        result.put("agentsDir", agentsDir.toString());

        Map<String, Object> mergedArgs = mergeArgs(args);

        String agentId = readString(mergedArgs, "agentId", "id", "name");
        if (agentId == null || agentId.isBlank()) {
            return failure(result, "Missing argument: agentId");
        }
        String normalizedAgentId = agentId.trim();
        if (!AGENT_ID_PATTERN.matcher(normalizedAgentId).matches()) {
            return failure(result, "Invalid agentId. Use [A-Za-z0-9_-], max 64 chars.");
        }

        AgentRuntimeMode mode;
        try {
            mode = AgentRuntimeMode.fromJson(readString(mergedArgs, "mode"));
        } catch (Exception ex) {
            return failure(result, "Invalid mode. Use one of PLAIN/THINKING/PLAIN_TOOLING/THINKING_TOOLING/REACT/PLAN_EXECUTE");
        }
        if (mode == null) {
            mode = AgentRuntimeMode.PLAIN;
        }

        String description = normalizeText(readString(mergedArgs, "description"), DEFAULT_DESCRIPTION);
        String providerKey = normalizeProviderKey(readString(mergedArgs, "providerKey", "providerType"));
        String model = normalizeText(readString(mergedArgs, "model"), DEFAULT_MODEL);

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("description", description);
        root.put("providerKey", providerKey);
        root.put("model", model);
        root.put("mode", mode.name());

        ArrayNode toolsNode = normalizeTools(mergedArgs.get("tools"));
        if (!toolsNode.isEmpty()) {
            root.set("tools", toolsNode);
        }

        putOptionalEnum(root, "compute", readString(mergedArgs, "compute"));
        putOptionalEnum(root, "output", readString(mergedArgs, "output"));
        putOptionalEnum(root, "toolPolicy", readString(mergedArgs, "toolPolicy"));
        putOptionalEnum(root, "verify", readString(mergedArgs, "verify"));
        putOptionalBudget(root, mergedArgs.get("budget"));

        ObjectNode modeConfig = buildModeConfig(mode, mergedArgs);
        if (modeConfig == null) {
            return failure(result, "Missing required mode prompt fields");
        }
        root.set(modeConfig.fieldNames().next(), modeConfig.elements().next());

        Path file = agentsDir.resolve(normalizedAgentId + ".json").normalize();
        if (!file.startsWith(agentsDir)) {
            return failure(result, "Resolved path escapes agents directory");
        }

        try {
            Files.createDirectories(agentsDir);
            boolean existed = Files.exists(file);
            Files.writeString(file, OBJECT_MAPPER.writerWithDefaultPrettyPrinter().writeValueAsString(root) + "\n");

            result.put("ok", true);
            result.put("created", !existed);
            result.put("updated", existed);
            result.put("agentId", normalizedAgentId);
            result.put("file", file.toString());
            result.set("config", root);
            return result;
        } catch (IOException ex) {
            return failure(result, "Write failed: " + ex.getMessage());
        }
    }

    private Map<String, Object> mergeArgs(Map<String, Object> args) {
        Map<String, Object> merged = new LinkedHashMap<>();
        if (args != null) {
            merged.putAll(args);
        }
        Object configObject = merged.get("config");
        if (configObject instanceof Map<?, ?> configMap) {
            for (Map.Entry<?, ?> entry : configMap.entrySet()) {
                if (entry.getKey() instanceof String key) {
                    merged.putIfAbsent(key, entry.getValue());
                }
            }
        }
        return merged;
    }

    private ObjectNode buildModeConfig(AgentRuntimeMode mode, Map<String, Object> args) {
        return switch (mode) {
            case PLAIN -> singlePromptConfig("plain", "systemPrompt", readString(args, "systemPrompt", "plainSystemPrompt"), null);
            case THINKING -> singlePromptConfig(
                    "thinking",
                    "systemPrompt",
                    readString(args, "systemPrompt", "thinkingSystemPrompt"),
                    parseBooleanNode(args.get("exposeReasoningToUser"))
            );
            case PLAIN_TOOLING -> singlePromptConfig("plainTooling", "systemPrompt", readString(args, "systemPrompt", "plainToolingSystemPrompt"), null);
            case THINKING_TOOLING -> singlePromptConfig(
                    "thinkingTooling",
                    "systemPrompt",
                    readString(args, "systemPrompt", "thinkingToolingSystemPrompt"),
                    parseBooleanNode(args.get("exposeReasoningToUser"))
            );
            case REACT -> reactConfig(readString(args, "systemPrompt", "reactSystemPrompt"), args.get("maxSteps"));
            case PLAN_EXECUTE -> planExecuteConfig(
                    readString(args, "planSystemPrompt"),
                    readString(args, "executeSystemPrompt"),
                    readString(args, "summarySystemPrompt")
            );
        };
    }

    private ObjectNode singlePromptConfig(String fieldName, String promptField, String prompt, Boolean exposeReasoningToUser) {
        String normalizedPrompt = normalizeText(prompt, DEFAULT_PROMPT);
        if (normalizedPrompt.isBlank()) {
            return null;
        }
        ObjectNode wrapper = OBJECT_MAPPER.createObjectNode();
        ObjectNode node = OBJECT_MAPPER.createObjectNode();
        node.put(promptField, normalizedPrompt);
        if (exposeReasoningToUser != null) {
            node.put("exposeReasoningToUser", exposeReasoningToUser);
        }
        wrapper.set(fieldName, node);
        return wrapper;
    }

    private ObjectNode reactConfig(String prompt, Object maxSteps) {
        String normalizedPrompt = normalizeText(prompt, DEFAULT_PROMPT);
        if (normalizedPrompt.isBlank()) {
            return null;
        }
        ObjectNode wrapper = OBJECT_MAPPER.createObjectNode();
        ObjectNode node = OBJECT_MAPPER.createObjectNode();
        node.put("systemPrompt", normalizedPrompt);
        if (maxSteps instanceof Number number && number.intValue() > 0) {
            node.put("maxSteps", number.intValue());
        }
        wrapper.set("react", node);
        return wrapper;
    }

    private ObjectNode planExecuteConfig(String planPrompt, String executePrompt, String summaryPrompt) {
        String normalizedPlan = normalizeText(planPrompt, "");
        String normalizedExecute = normalizeText(executePrompt, "");
        if (normalizedPlan.isBlank() || normalizedExecute.isBlank()) {
            return null;
        }
        ObjectNode wrapper = OBJECT_MAPPER.createObjectNode();
        ObjectNode node = OBJECT_MAPPER.createObjectNode();
        node.put("planSystemPrompt", normalizedPlan);
        node.put("executeSystemPrompt", normalizedExecute);
        String normalizedSummary = normalizeText(summaryPrompt, "");
        if (!normalizedSummary.isBlank()) {
            node.put("summarySystemPrompt", normalizedSummary);
        }
        wrapper.set("planExecute", node);
        return wrapper;
    }

    private Boolean parseBooleanNode(Object value) {
        if (value instanceof Boolean bool) {
            return bool;
        }
        if (value instanceof String text && !text.isBlank()) {
            return Boolean.parseBoolean(text.trim());
        }
        return null;
    }

    private ArrayNode normalizeTools(Object rawTools) {
        ArrayNode arrayNode = OBJECT_MAPPER.createArrayNode();
        if (!(rawTools instanceof List<?> list)) {
            return arrayNode;
        }
        for (Object item : list) {
            if (item == null) {
                continue;
            }
            String tool = item.toString().trim().toLowerCase(Locale.ROOT);
            if (!tool.isBlank()) {
                arrayNode.add(tool);
            }
        }
        return arrayNode;
    }

    private void putOptionalEnum(ObjectNode root, String fieldName, String value) {
        if (value == null || value.isBlank()) {
            return;
        }
        root.put(fieldName, value.trim().toUpperCase(Locale.ROOT));
    }

    private void putOptionalBudget(ObjectNode root, Object budgetValue) {
        if (!(budgetValue instanceof Map<?, ?> map)) {
            return;
        }
        ObjectNode budget = OBJECT_MAPPER.createObjectNode();
        putIntIfPositive(budget, "maxModelCalls", map.get("maxModelCalls"));
        putIntIfPositive(budget, "maxToolCalls", map.get("maxToolCalls"));
        putIntIfPositive(budget, "maxSteps", map.get("maxSteps"));
        putLongIfPositive(budget, "timeoutMs", map.get("timeoutMs"));
        if (!budget.isEmpty()) {
            root.set("budget", budget);
        }
    }

    private void putIntIfPositive(ObjectNode node, String field, Object value) {
        if (value instanceof Number number && number.intValue() > 0) {
            node.put(field, number.intValue());
        }
    }

    private void putLongIfPositive(ObjectNode node, String field, Object value) {
        if (value instanceof Number number && number.longValue() > 0) {
            node.put(field, number.longValue());
        }
    }

    private String normalizeProviderKey(String raw) {
        if (raw == null || raw.isBlank()) {
            return DEFAULT_PROVIDER_KEY;
        }
        return raw.trim().toLowerCase(Locale.ROOT);
    }

    private String readString(Map<String, Object> args, String... keys) {
        for (String key : keys) {
            Object value = args.get(key);
            if (value != null) {
                return value.toString();
            }
        }
        return null;
    }

    private String normalizeText(String value, String fallback) {
        if (value == null || value.isBlank()) {
            return fallback;
        }
        return value.trim();
    }

    private JsonNode failure(ObjectNode root, String error) {
        root.put("ok", false);
        root.put("error", error);
        return root;
    }
}
