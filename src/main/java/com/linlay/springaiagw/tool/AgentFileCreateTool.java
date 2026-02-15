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
    private static final String DEFAULT_DESCRIPTION = "由 demoAgentCreator 创建的智能体";
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

        String key = readString(mergedArgs, "key", "agentId", "id", "name");
        if (key == null || key.isBlank()) {
            return failure(result, "Missing argument: key");
        }
        String normalizedKey = key.trim();
        if (!AGENT_ID_PATTERN.matcher(normalizedKey).matches()) {
            return failure(result, "Invalid agentId/key. Use [A-Za-z0-9_-], max 64 chars.");
        }

        AgentRuntimeMode mode;
        try {
            mode = AgentRuntimeMode.fromJson(readString(mergedArgs, "mode"));
        } catch (Exception ex) {
            return failure(result, "Invalid mode. Use one of ONESHOT/REACT/PLAN_EXECUTE");
        }
        if (mode == null) {
            mode = AgentRuntimeMode.ONESHOT;
        }

        String description = normalizeText(readString(mergedArgs, "description"), DEFAULT_DESCRIPTION);
        String name = normalizeText(readString(mergedArgs, "name"), normalizedKey);
        String icon = normalizeText(readString(mergedArgs, "icon"), "");

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("key", normalizedKey);
        root.put("name", name);
        if (!icon.isBlank()) {
            root.put("icon", icon);
        }
        root.put("description", description);
        root.put("mode", mode.name());

        ObjectNode modelConfig = buildModelConfig(mergedArgs);
        root.set("modelConfig", modelConfig);

        ObjectNode toolConfig = buildToolConfig(mergedArgs);
        if (toolConfig != null) {
            root.set("toolConfig", toolConfig);
        }
        ObjectNode skillConfig = buildSkillConfig(mergedArgs);
        if (skillConfig != null) {
            root.set("skillConfig", skillConfig);
        }

        putOptionalEnum(root, "output", readString(mergedArgs, "output"));
        putOptionalEnum(root, "toolPolicy", readString(mergedArgs, "toolPolicy"));
        putOptionalEnum(root, "verify", readString(mergedArgs, "verify"));
        putOptionalBudget(root, mergedArgs.get("budget"));

        ObjectNode modeConfig = buildModeConfig(mode, mergedArgs);
        if (modeConfig == null) {
            return failure(result, "Missing required mode prompt fields");
        }
        root.setAll(modeConfig);

        Path file = agentsDir.resolve(normalizedKey + ".json").normalize();
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
            result.put("agentId", normalizedKey);
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
            case ONESHOT -> oneshotConfig(readString(args, "systemPrompt", "plainSystemPrompt"));
            case REACT -> reactConfig(readString(args, "systemPrompt", "reactSystemPrompt"), args.get("maxSteps"));
            case PLAN_EXECUTE -> planExecuteConfig(
                    readString(args, "planSystemPrompt"),
                    readString(args, "executeSystemPrompt"),
                    readString(args, "summarySystemPrompt")
            );
        };
    }

    private ObjectNode oneshotConfig(String prompt) {
        String normalizedPrompt = normalizeText(prompt, DEFAULT_PROMPT);
        if (normalizedPrompt.isBlank()) {
            return null;
        }
        ObjectNode wrapper = OBJECT_MAPPER.createObjectNode();
        ObjectNode node = OBJECT_MAPPER.createObjectNode();
        node.put("systemPrompt", normalizedPrompt);
        wrapper.set("plain", node);
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
        ObjectNode plan = OBJECT_MAPPER.createObjectNode();
        plan.put("systemPrompt", normalizedPlan);
        node.set("plan", plan);
        ObjectNode execute = OBJECT_MAPPER.createObjectNode();
        execute.put("systemPrompt", normalizedExecute);
        node.set("execute", execute);
        String normalizedSummary = normalizeText(summaryPrompt, "");
        if (!normalizedSummary.isBlank()) {
            ObjectNode summary = OBJECT_MAPPER.createObjectNode();
            summary.put("systemPrompt", normalizedSummary);
            node.set("summary", summary);
        }
        wrapper.set("planExecute", node);
        return wrapper;
    }

    private ObjectNode parseReasoning(Map<String, Object> args) {
        Object enabledRaw = args.get("reasoningEnabled");
        Object effortRaw = args.get("reasoningEffort");
        if (enabledRaw == null && effortRaw == null) {
            return null;
        }
        ObjectNode node = OBJECT_MAPPER.createObjectNode();
        if (enabledRaw instanceof Boolean enabled) {
            node.put("enabled", enabled);
        } else if (enabledRaw instanceof String text && !text.isBlank()) {
            node.put("enabled", Boolean.parseBoolean(text.trim()));
        }
        if (effortRaw instanceof String effort && !effort.isBlank()) {
            node.put("effort", effort.trim().toUpperCase(Locale.ROOT));
        }
        if (node.isEmpty()) {
            return null;
        }
        return node;
    }

    private ObjectNode buildModelConfig(Map<String, Object> args) {
        if (args.get("modelConfig") instanceof Map<?, ?> raw) {
            ObjectNode fromObject = modelConfigFromMap(raw);
            if (fromObject != null) {
                return fromObject;
            }
        }
        ObjectNode modelConfig = OBJECT_MAPPER.createObjectNode();
        String providerKey = normalizeProviderKey(readString(args, "providerKey", "providerType"));
        String model = normalizeText(readString(args, "model"), DEFAULT_MODEL);
        modelConfig.put("providerKey", providerKey);
        modelConfig.put("model", model);
        ObjectNode reasoning = parseReasoning(args);
        if (reasoning != null) {
            modelConfig.set("reasoning", reasoning);
        }
        putDecimal(modelConfig, "temperature", args.get("temperature"));
        putDecimal(modelConfig, "top_p", args.get("top_p"));
        putInt(modelConfig, "max_tokens", args.get("max_tokens"));
        return modelConfig;
    }

    private ObjectNode modelConfigFromMap(Map<?, ?> raw) {
        if (raw == null) {
            return null;
        }
        ObjectNode modelConfig = OBJECT_MAPPER.createObjectNode();
        String providerKey = raw.get("providerKey") == null ? null : raw.get("providerKey").toString();
        String model = raw.get("model") == null ? null : raw.get("model").toString();
        modelConfig.put("providerKey", normalizeProviderKey(providerKey));
        modelConfig.put("model", normalizeText(model, DEFAULT_MODEL));

        Object reasoningRaw = raw.get("reasoning");
        if (reasoningRaw instanceof Map<?, ?> reasoningMap) {
            ObjectNode reasoning = OBJECT_MAPPER.createObjectNode();
            Object enabled = reasoningMap.get("enabled");
            Object effort = reasoningMap.get("effort");
            if (enabled instanceof Boolean value) {
                reasoning.put("enabled", value);
            } else if (enabled instanceof String text && !text.isBlank()) {
                reasoning.put("enabled", Boolean.parseBoolean(text.trim()));
            }
            if (effort instanceof String text && !text.isBlank()) {
                reasoning.put("effort", text.trim().toUpperCase(Locale.ROOT));
            }
            if (!reasoning.isEmpty()) {
                modelConfig.set("reasoning", reasoning);
            }
        }

        putDecimal(modelConfig, "temperature", raw.get("temperature"));
        putDecimal(modelConfig, "top_p", raw.get("top_p"));
        putInt(modelConfig, "max_tokens", raw.get("max_tokens"));
        return modelConfig;
    }

    private ObjectNode buildToolConfig(Map<String, Object> args) {
        ObjectNode fromConfig = buildToolConfigFromObject(args.get("toolConfig"));
        if (fromConfig != null) {
            return fromConfig;
        }

        ArrayNode backends = normalizeTools(args.get("tools"));
        ArrayNode frontends = normalizeTools(args.get("frontends"));
        ArrayNode actions = normalizeTools(args.get("actions"));

        if (backends.isEmpty() && frontends.isEmpty() && actions.isEmpty()) {
            return null;
        }
        ObjectNode toolConfig = OBJECT_MAPPER.createObjectNode();
        toolConfig.set("backends", backends);
        toolConfig.set("frontends", frontends);
        toolConfig.set("actions", actions);
        return toolConfig;
    }

    private ObjectNode buildSkillConfig(Map<String, Object> args) {
        ObjectNode fromConfig = buildSkillConfigFromObject(args.get("skillConfig"));
        if (fromConfig != null) {
            return fromConfig;
        }
        ArrayNode skills = normalizeTools(args.get("skills"));
        if (skills.isEmpty()) {
            return null;
        }
        ObjectNode skillConfig = OBJECT_MAPPER.createObjectNode();
        skillConfig.set("skills", skills);
        return skillConfig;
    }

    private ObjectNode buildSkillConfigFromObject(Object raw) {
        if (!(raw instanceof Map<?, ?> map)) {
            return null;
        }
        ArrayNode skills = normalizeTools(map.get("skills"));
        if (skills.isEmpty()) {
            return null;
        }
        ObjectNode skillConfig = OBJECT_MAPPER.createObjectNode();
        skillConfig.set("skills", skills);
        return skillConfig;
    }

    private ObjectNode buildToolConfigFromObject(Object raw) {
        if (!(raw instanceof Map<?, ?> map)) {
            return null;
        }
        ArrayNode backends = normalizeTools(map.get("backends"));
        ArrayNode frontends = normalizeTools(map.get("frontends"));
        ArrayNode actions = normalizeTools(map.get("actions"));
        ObjectNode toolConfig = OBJECT_MAPPER.createObjectNode();
        toolConfig.set("backends", backends);
        toolConfig.set("frontends", frontends);
        toolConfig.set("actions", actions);
        return toolConfig;
    }

    private ArrayNode normalizeTools(Object rawTools) {
        ArrayNode array = OBJECT_MAPPER.createArrayNode();
        if (!(rawTools instanceof List<?> list)) {
            return array;
        }
        for (Object item : list) {
            if (item == null) {
                continue;
            }
            String tool = item.toString().trim().toLowerCase(Locale.ROOT);
            if (!tool.isBlank()) {
                array.add(tool);
            }
        }
        return array;
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

    private void putDecimal(ObjectNode node, String field, Object value) {
        if (value instanceof Number number) {
            node.put(field, number.doubleValue());
        } else if (value instanceof String text && !text.isBlank()) {
            try {
                node.put(field, Double.parseDouble(text.trim()));
            } catch (NumberFormatException ignored) {
                // ignore invalid decimal
            }
        }
    }

    private void putInt(ObjectNode node, String field, Object value) {
        if (value instanceof Number number) {
            node.put(field, number.intValue());
        } else if (value instanceof String text && !text.isBlank()) {
            try {
                node.put(field, Integer.parseInt(text.trim()));
            } catch (NumberFormatException ignored) {
                // ignore invalid int
            }
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
