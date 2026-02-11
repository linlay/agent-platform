package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.agent.AgentCatalogProperties;
import com.linlay.springaiagw.agent.AgentMode;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Pattern;

@Component
public class AgentFileCreateTool extends AbstractDeterministicTool {

    private static final Pattern AGENT_ID_PATTERN = Pattern.compile("^[A-Za-z0-9_-]{1,64}$");
    private static final String DEFAULT_DESCRIPTION = "由 agentCreator 创建的智能体";
    private static final String DEFAULT_MODEL = "qwen3-max";
    private static final String DEFAULT_SYSTEM_PROMPT = "你是通用助理，回答要清晰和可执行。";
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
    public String description() {
        return "创建或更新 agents 目录下的智能体配置文件。参数: agentId,description,providerType,model,systemPrompt,deepThink";
    }

    @Override
    public Map<String, Object> parametersSchema() {
        return Map.of(
                "type", "object",
                "properties", Map.of(
                        "agentId", Map.of("type", "string", "description", "目标 agentId"),
                        "description", Map.of("type", "string"),
                        "providerType", Map.of("type", "string"),
                        "model", Map.of("type", "string"),
                        "systemPrompt", Map.of("type", "string"),
                        "deepThink", Map.of("type", "boolean"),
                        "mode", Map.of("type", "string"),
                        "config", Map.of(
                                "type", "object",
                                "description", "可选，支持把 agent 配置放在 config 对象中"
                        )
                ),
                "required", List.of("agentId"),
                "additionalProperties", false
        );
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        ObjectNode result = OBJECT_MAPPER.createObjectNode();
        result.put("tool", name());
        result.put("agentsDir", agentsDir.toString());

        Map<String, Object> mergedArgs = new LinkedHashMap<>();
        if (args != null) {
            mergedArgs.putAll(args);
        }
        mergeConfigField(mergedArgs);

        String agentId = readString(mergedArgs, "agentId", "id", "name");
        if (agentId == null || agentId.isBlank()) {
            return failure(result, "Missing argument: agentId");
        }
        String normalizedAgentId = agentId.trim();
        if (!AGENT_ID_PATTERN.matcher(normalizedAgentId).matches()) {
            return failure(result, "Invalid agentId. Use [A-Za-z0-9_-], max 64 chars.");
        }

        String description = normalizeText(readString(mergedArgs, "description"), DEFAULT_DESCRIPTION);
        String model = normalizeText(readString(mergedArgs, "model"), DEFAULT_MODEL);
        String systemPrompt = normalizeText(readString(mergedArgs, "systemPrompt", "prompt"), DEFAULT_SYSTEM_PROMPT);

        String providerType = normalizeProviderType(readString(mergedArgs, "providerType"));
        boolean deepThink = parseDeepThink(mergedArgs);

        ObjectNode config = OBJECT_MAPPER.createObjectNode();
        config.put("description", description);
        config.put("providerType", providerType);
        config.put("model", model);
        config.put("systemPrompt", systemPrompt);
        config.put("deepThink", deepThink);

        Path file = agentsDir.resolve(normalizedAgentId + ".json").normalize();
        if (!file.startsWith(agentsDir)) {
            return failure(result, "Resolved path escapes agents directory");
        }

        try {
            Files.createDirectories(agentsDir);
            boolean existed = Files.exists(file);
            Files.writeString(file, toAgentConfigFileContent(description, providerType, model, systemPrompt, deepThink));

            result.put("ok", true);
            result.put("created", !existed);
            result.put("updated", existed);
            result.put("agentId", normalizedAgentId);
            result.put("file", file.toString());
            result.set("config", config);
            return result;
        } catch (IOException ex) {
            return failure(result, "Write failed: " + ex.getMessage());
        }
    }

    private void mergeConfigField(Map<String, Object> mergedArgs) {
        Object configObject = mergedArgs.get("config");
        if (configObject instanceof Map<?, ?> configMap) {
            for (Map.Entry<?, ?> entry : configMap.entrySet()) {
                if (entry.getKey() instanceof String key) {
                    mergedArgs.putIfAbsent(key, entry.getValue());
                }
            }
        }
    }

    private String normalizeProviderType(String raw) {
        if (raw == null || raw.isBlank()) {
            return "BAILIAN";
        }
        return raw.trim().toUpperCase(Locale.ROOT);
    }

    private boolean parseDeepThink(Map<String, Object> args) {
        Object deepThinkRaw = args.get("deepThink");
        if (deepThinkRaw != null) {
            if (deepThinkRaw instanceof Boolean value) {
                return value;
            }
            String text = deepThinkRaw.toString().trim();
            if ("true".equalsIgnoreCase(text)) {
                return true;
            }
            if ("false".equalsIgnoreCase(text)) {
                return false;
            }
        }

        String rawMode = readString(args, "mode");
        if (rawMode == null || rawMode.isBlank()) {
            return false;
        }

        try {
            AgentMode mode = AgentMode.fromJson(rawMode);
            return mode == AgentMode.PLAN_EXECUTE;
        } catch (Exception ex) {
            return false;
        }
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

    private String toAgentConfigFileContent(
            String description,
            String providerType,
            String model,
            String systemPrompt,
            boolean deepThink
    ) throws JsonProcessingException {
        StringBuilder builder = new StringBuilder();
        builder.append("{\n");
        builder.append("  \"description\": ").append(jsonString(description)).append(",\n");
        builder.append("  \"providerType\": ").append(jsonString(providerType)).append(",\n");
        builder.append("  \"model\": ").append(jsonString(model)).append(",\n");
        builder.append("  \"systemPrompt\": ").append(systemPromptValue(systemPrompt)).append(",\n");
        builder.append("  \"deepThink\": ").append(deepThink).append("\n");
        builder.append("}\n");
        return builder.toString();
    }

    private String systemPromptValue(String systemPrompt) throws JsonProcessingException {
        String normalized = systemPrompt == null ? "" : systemPrompt.replace("\r\n", "\n");
        if (normalized.contains("\n") && !normalized.contains("\"\"\"")) {
            return "\"\"\"\n" + normalized + "\n\"\"\"";
        }
        return jsonString(normalized);
    }

    private String jsonString(String text) throws JsonProcessingException {
        return OBJECT_MAPPER.writeValueAsString(text);
    }

    private JsonNode failure(ObjectNode root, String error) {
        root.put("ok", false);
        root.put("error", error);
        return root;
    }
}
