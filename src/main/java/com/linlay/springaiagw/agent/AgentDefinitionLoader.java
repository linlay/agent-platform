package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ModePresetMapper;
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
import java.util.List;
import java.util.Locale;
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

    @Deprecated
    public AgentDefinitionLoader(ObjectMapper objectMapper, AgentCatalogProperties properties) {
        this(objectMapper, properties, null);
    }

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

        List<AgentDefinition> loaded = new ArrayList<>();
        try (Stream<Path> stream = Files.list(dir)) {
            stream.filter(path -> Files.isRegularFile(path) && path.getFileName().toString().endsWith(".json"))
                    .sorted(Comparator.comparing(path -> path.getFileName().toString()))
                    .forEach(path -> tryLoadExternal(path).ifPresent(loaded::add));
        } catch (IOException ex) {
            log.warn("Cannot list external agents from {}", dir, ex);
        }

        if (!loaded.isEmpty()) {
            log.debug("Loaded {} external agents from {}", loaded.size(), dir);
        }
        return loaded;
    }

    private java.util.Optional<AgentDefinition> tryLoadExternal(Path file) {
        String fileName = file.getFileName().toString();
        String agentId = fileName.substring(0, fileName.length() - ".json".length()).trim();
        if (agentId.isEmpty()) {
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
            String model = normalize(config.getModel(), resolveDefaultModel(providerKey));
            String description = normalize(config.getDescription(), "external agent from " + fileName);
            List<String> tools = normalizeToolNames(config.getTools());
            RunSpec runSpec = ModePresetMapper.toRunSpec(mode, config);
            AgentPromptSet promptSet = resolvePromptSet(mode, config, file);
            if (promptSet == null) {
                return java.util.Optional.empty();
            }

            return java.util.Optional.of(new AgentDefinition(
                    agentId,
                    description,
                    providerKey,
                    model,
                    mode,
                    runSpec,
                    promptSet,
                    tools
            ));
        } catch (Exception ex) {
            log.warn("Skip invalid external agent file: {}", file, ex);
            return java.util.Optional.empty();
        }
    }

    private AgentPromptSet resolvePromptSet(AgentRuntimeMode mode, AgentConfigFile config, Path file) {
        return switch (mode) {
            case PLAIN -> {
                String prompt = normalize(config.getPlain() == null ? null : config.getPlain().getSystemPrompt(), "");
                if (prompt.isBlank()) {
                    log.warn("Skip agent {}: plain.systemPrompt is required", file);
                    yield null;
                }
                yield new AgentPromptSet(prompt, null, null, null);
            }
            case THINKING -> {
                String prompt = normalize(config.getThinking() == null ? null : config.getThinking().getSystemPrompt(), "");
                if (prompt.isBlank()) {
                    log.warn("Skip agent {}: thinking.systemPrompt is required", file);
                    yield null;
                }
                yield new AgentPromptSet(prompt, null, null, null);
            }
            case PLAIN_TOOLING -> {
                String prompt = normalize(config.getPlainTooling() == null ? null : config.getPlainTooling().getSystemPrompt(), "");
                if (prompt.isBlank()) {
                    log.warn("Skip agent {}: plainTooling.systemPrompt is required", file);
                    yield null;
                }
                yield new AgentPromptSet(prompt, null, null, null);
            }
            case THINKING_TOOLING -> {
                String prompt = normalize(config.getThinkingTooling() == null ? null : config.getThinkingTooling().getSystemPrompt(), "");
                if (prompt.isBlank()) {
                    log.warn("Skip agent {}: thinkingTooling.systemPrompt is required", file);
                    yield null;
                }
                yield new AgentPromptSet(prompt, null, null, null);
            }
            case REACT -> {
                String prompt = normalize(config.getReact() == null ? null : config.getReact().getSystemPrompt(), "");
                if (prompt.isBlank()) {
                    log.warn("Skip agent {}: react.systemPrompt is required", file);
                    yield null;
                }
                yield new AgentPromptSet(prompt, null, null, null);
            }
            case PLAN_EXECUTE -> {
                String planPrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getPlanSystemPrompt(), "");
                String executePrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getExecuteSystemPrompt(), "");
                String summaryPrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getSummarySystemPrompt(), "");
                if (planPrompt.isBlank() || executePrompt.isBlank()) {
                    log.warn("Skip agent {}: planExecute.planSystemPrompt and planExecute.executeSystemPrompt are required", file);
                    yield null;
                }
                yield new AgentPromptSet(executePrompt, planPrompt, executePrompt, summaryPrompt.isBlank() ? null : summaryPrompt);
            }
        };
    }

    private boolean isLegacyConfig(JsonNode root) {
        if (root == null || !root.isObject()) {
            return true;
        }
        return root.has("deepThink") || root.has("systemPrompt") || root.has("mode") && root.path("mode").isTextual()
                && isLegacyMode(root.path("mode").asText());
    }

    private boolean isLegacyMode(String mode) {
        if (mode == null || mode.isBlank()) {
            return false;
        }
        String normalized = mode.trim().replace('-', '_').toUpperCase(Locale.ROOT);
        return "RE_ACT".equals(normalized)
                || "PLAIN_CONTENT".equals(normalized)
                || "THINKING_AND_CONTENT".equals(normalized)
                || "THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS".equals(normalized);
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
        if (config.getProviderKey() != null && !config.getProviderKey().isBlank()) {
            return config.getProviderKey().trim().toLowerCase(Locale.ROOT);
        }
        if (config.getProviderType() != null && !config.getProviderType().isBlank()) {
            return config.getProviderType().trim().toLowerCase(Locale.ROOT);
        }
        return "bailian";
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
}
