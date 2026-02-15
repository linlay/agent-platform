package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.mode.AgentMode;
import com.linlay.springaiagw.agent.mode.AgentModeFactory;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.config.ChatClientRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Stream;

@Component
public class AgentDefinitionLoader {

    private static final Logger log = LoggerFactory.getLogger(AgentDefinitionLoader.class);
    private static final Pattern MULTILINE_PROMPT_PATTERN =
            Pattern.compile("(\"[a-zA-Z0-9_]*systemPrompt\"\\s*:\\s*)\"\"\"([\\s\\S]*?)\"\"\"", Pattern.CASE_INSENSITIVE);

    private final ObjectMapper objectMapper;
    private final AgentCatalogProperties properties;
    private final ChatClientRegistry chatClientRegistry;

    @Autowired
    public AgentDefinitionLoader(ObjectMapper objectMapper, AgentCatalogProperties properties, ChatClientRegistry chatClientRegistry) {
        this.objectMapper = objectMapper;
        this.properties = properties;
        this.chatClientRegistry = chatClientRegistry;
    }

    public List<AgentDefinition> loadAll() {
        return loadExternalAgents();
    }

    private List<AgentDefinition> loadExternalAgents() {
        Path dir = Path.of(properties.getExternalDir()).toAbsolutePath().normalize();
        if (!Files.exists(dir)) {
            log.debug("External agents dir does not exist, skip loading: {}", dir);
            return List.of();
        }
        if (!Files.isDirectory(dir)) {
            log.warn("Configured external agents path is not a directory: {}", dir);
            return List.of();
        }

        Map<String, AgentDefinition> loaded = new LinkedHashMap<>();
        try (Stream<Path> stream = Files.list(dir)) {
            stream.filter(path -> Files.isRegularFile(path) && path.getFileName().toString().endsWith(".json"))
                    .sorted(Comparator.comparing(path -> path.getFileName().toString()))
                    .forEach(path -> tryLoadExternal(path).ifPresent(definition -> {
                        if (loaded.containsKey(definition.id())) {
                            log.warn("Skip duplicated agent key '{}' from file {}", definition.id(), path);
                            return;
                        }
                        loaded.put(definition.id(), definition);
                    }));
        } catch (IOException ex) {
            log.warn("Cannot list external agents from {}", dir, ex);
        }

        if (!loaded.isEmpty()) {
            log.debug("Loaded {} external agents from {}", loaded.size(), dir);
        }
        return new ArrayList<>(loaded.values());
    }

    private java.util.Optional<AgentDefinition> tryLoadExternal(Path file) {
        String fileName = file.getFileName().toString();
        String fileBasedId = fileName.substring(0, fileName.length() - ".json".length()).trim();
        if (fileBasedId.isEmpty()) {
            log.warn("Skip external agent with empty name: {}", file);
            return java.util.Optional.empty();
        }

        try {
            String raw = Files.readString(file);
            String normalizedJson = normalizeMultilinePrompts(raw);
            JsonNode root = objectMapper.readTree(normalizedJson);
            if (isLegacyConfig(root)) {
                log.warn("Skip legacy agent config {}. Only Agent JSON v2 is supported.", file);
                return java.util.Optional.empty();
            }

            AgentConfigFile config = objectMapper.treeToValue(root, AgentConfigFile.class);
            AgentRuntimeMode mode = config.getMode();
            if (mode == null) {
                log.warn("Skip agent without mode in {}", file);
                return java.util.Optional.empty();
            }

            String providerKey = resolveProviderKey(config);
            String model = resolveModel(config, providerKey);
            String key = normalize(config.getKey(), fileBasedId);
            String name = normalize(config.getName(), key);
            String icon = normalizeIcon(config.getIcon());
            String description = normalize(config.getDescription(), "external agent from " + fileName);
            List<String> tools = collectToolNames(config);

            AgentMode agentMode = AgentModeFactory.create(mode, config, file);
            RunSpec runSpec = agentMode.defaultRunSpec(config);

            return java.util.Optional.of(new AgentDefinition(
                    key,
                    name,
                    icon,
                    description,
                    providerKey,
                    model,
                    mode,
                    runSpec,
                    agentMode,
                    tools
            ));
        } catch (Exception ex) {
            log.warn("Skip invalid external agent file: {}", file, ex);
            return java.util.Optional.empty();
        }
    }

    private boolean isLegacyConfig(JsonNode root) {
        if (root == null || !root.isObject()) {
            return true;
        }
        if (root.has("deepThink")
                || root.has("systemPrompt")
                || root.has("providerKey")
                || root.has("providerType")
                || root.has("model")
                || root.has("reasoning")
                || root.has("tools")) {
            return true;
        }
        if (!root.has("modelConfig")) {
            return true;
        }
        return hasLegacyStageFields(root.path("plain"))
                || hasLegacyStageFields(root.path("react"))
                || hasLegacyPlanExecuteStageFields(root.path("planExecute"));
    }

    private boolean hasLegacyPlanExecuteStageFields(JsonNode node) {
        if (node == null || !node.isObject()) {
            return false;
        }
        return hasLegacyStageFields(node.path("plan"))
                || hasLegacyStageFields(node.path("execute"))
                || hasLegacyStageFields(node.path("summary"));
    }

    private boolean hasLegacyStageFields(JsonNode node) {
        if (node == null || !node.isObject()) {
            return false;
        }
        return node.has("providerKey")
                || node.has("providerType")
                || node.has("model")
                || node.has("reasoning")
                || node.has("tools");
    }

    private String normalizeMultilinePrompts(String rawJson) throws IOException {
        Matcher matcher = MULTILINE_PROMPT_PATTERN.matcher(rawJson);
        if (!matcher.find()) {
            return rawJson;
        }

        StringBuffer rewritten = new StringBuffer();
        do {
            String content = stripOuterLineBreak(matcher.group(2));
            String escaped = objectMapper.writeValueAsString(content);
            matcher.appendReplacement(rewritten, Matcher.quoteReplacement(matcher.group(1) + escaped));
        } while (matcher.find());
        matcher.appendTail(rewritten);
        return rewritten.toString();
    }

    private String stripOuterLineBreak(String content) {
        String normalized = content.replace("\r\n", "\n");
        if (normalized.startsWith("\n")) {
            normalized = normalized.substring(1);
        }
        if (normalized.endsWith("\n")) {
            normalized = normalized.substring(0, normalized.length() - 1);
        }
        return normalized;
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    private String resolveProviderKey(AgentConfigFile config) {
        AgentConfigFile.ModelConfig modelConfig = config == null ? null : config.getModelConfig();
        if (modelConfig != null && modelConfig.getProviderKey() != null && !modelConfig.getProviderKey().isBlank()) {
            return modelConfig.getProviderKey().trim().toLowerCase(Locale.ROOT);
        }
        return "bailian";
    }

    private String resolveModel(AgentConfigFile config, String providerKey) {
        AgentConfigFile.ModelConfig modelConfig = config == null ? null : config.getModelConfig();
        String configured = modelConfig == null ? null : modelConfig.getModel();
        return normalize(configured, resolveDefaultModel(providerKey));
    }

    private String resolveDefaultModel(String providerKey) {
        if (chatClientRegistry != null) {
            String dynamicModel = chatClientRegistry.defaultModel(providerKey);
            if (dynamicModel != null && !dynamicModel.isBlank()) {
                return dynamicModel;
            }
        }
        if ("siliconflow".equalsIgnoreCase(providerKey)) {
            return "deepseek-ai/DeepSeek-V3.2";
        }
        return "qwen3-max";
    }

    private List<String> normalizeToolNames(List<String> rawTools) {
        if (rawTools == null || rawTools.isEmpty()) {
            return List.of();
        }
        List<String> tools = new ArrayList<>();
        for (String raw : rawTools) {
            if (raw == null || raw.isBlank()) {
                continue;
            }
            tools.add(raw.trim().toLowerCase(Locale.ROOT));
        }
        return List.copyOf(tools);
    }

    private String normalizeIcon(String icon) {
        if (icon == null || icon.isBlank()) {
            return null;
        }
        return icon.trim();
    }

    private List<String> collectToolNames(AgentConfigFile config) {
        List<String> merged = new ArrayList<>(toolNames(config == null ? null : config.getToolConfig()));
        if (config.getPlain() != null) {
            merged.addAll(toolNames(config.getPlain().getToolConfig()));
        }
        if (config.getReact() != null) {
            merged.addAll(toolNames(config.getReact().getToolConfig()));
        }
        if (config.getPlanExecute() != null) {
            if (config.getPlanExecute().getPlan() != null) {
                merged.addAll(toolNames(config.getPlanExecute().getPlan().getToolConfig()));
            }
            if (config.getPlanExecute().getExecute() != null) {
                merged.addAll(toolNames(config.getPlanExecute().getExecute().getToolConfig()));
            }
            if (config.getPlanExecute().getSummary() != null) {
                merged.addAll(toolNames(config.getPlanExecute().getSummary().getToolConfig()));
            }
        }
        return merged.stream().distinct().toList();
    }

    private List<String> toolNames(AgentConfigFile.ToolConfig toolConfig) {
        if (toolConfig == null) {
            return List.of();
        }
        List<String> merged = new ArrayList<>();
        merged.addAll(normalizeToolNames(toolConfig.getBackends()));
        merged.addAll(normalizeToolNames(toolConfig.getFrontends()));
        merged.addAll(normalizeToolNames(toolConfig.getActions()));
        return merged;
    }
}
