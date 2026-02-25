package com.linlay.agentplatform.agent;

import com.fasterxml.jackson.core.json.JsonReadFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.mode.AgentMode;
import com.linlay.agentplatform.agent.mode.AgentModeFactory;
import com.linlay.agentplatform.agent.runtime.AgentRuntimeMode;
import com.linlay.agentplatform.agent.runtime.policy.RunSpec;
import com.linlay.agentplatform.model.ModelDefinition;
import com.linlay.agentplatform.model.ModelProtocol;
import com.linlay.agentplatform.model.ModelRegistryService;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;
import org.springframework.util.StringUtils;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Stream;

@Component
public class AgentDefinitionLoader {

    private static final Logger log = LoggerFactory.getLogger(AgentDefinitionLoader.class);
    private static final String PLAN_ADD_TASK_TOOL = "_plan_add_tasks_";
    private static final String PLAN_UPDATE_TASK_TOOL = "_plan_update_task_";
    private static final Pattern MULTILINE_PROMPT_PATTERN =
            Pattern.compile("(\"[a-zA-Z0-9_]*systemPrompt\"\\s*:\\s*)\"\"\"([\\s\\S]*?)\"\"\"", Pattern.CASE_INSENSITIVE);

    private final ObjectMapper objectMapper;
    private final AgentCatalogProperties properties;
    private final ModelRegistryService modelRegistryService;

    @Autowired
    public AgentDefinitionLoader(
            ObjectMapper objectMapper,
            AgentCatalogProperties properties,
            ModelRegistryService modelRegistryService
    ) {
        this.objectMapper = objectMapper;
        this.properties = properties;
        this.modelRegistryService = modelRegistryService;
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

    private Optional<AgentDefinition> tryLoadExternal(Path file) {
        String fileName = file.getFileName().toString();
        String fileBasedId = fileName.substring(0, fileName.length() - ".json".length()).trim();
        if (fileBasedId.isEmpty()) {
            log.warn("Skip external agent with empty name: {}", file);
            return Optional.empty();
        }

        try {
            String raw = Files.readString(file);
            String normalizedJson = normalizeMultilinePrompts(raw);
            JsonNode root = objectMapper
                    .reader()
                    .with(JsonReadFeature.ALLOW_JAVA_COMMENTS.mappedFeature())
                    .with(JsonReadFeature.ALLOW_YAML_COMMENTS.mappedFeature())
                    .readTree(normalizedJson);
            if (isLegacyConfig(root)) {
                log.warn("Skip legacy agent config {}. Only Agent JSON v2 is supported.", file);
                return Optional.empty();
            }
            if (hasRemovedFields(root)) {
                log.warn("Skip agent config {}. Removed fields are no longer supported.", file);
                return Optional.empty();
            }

            AgentConfigFile config = objectMapper.treeToValue(root, AgentConfigFile.class);
            AgentRuntimeMode mode = config.getMode();
            if (mode == null) {
                log.warn("Skip agent without mode in {}", file);
                return Optional.empty();
            }
            if (!hasAnyModelConfig(config)) {
                log.warn("Skip agent without modelConfig (top-level or stage-level) in {}", file);
                return Optional.empty();
            }

            ModelDefinition primaryModel = resolvePrimaryModel(config).orElse(null);
            if (primaryModel == null) {
                log.warn("Skip agent without resolvable modelKey in {}", file);
                return Optional.empty();
            }
            String key = normalize(config.getKey(), fileBasedId);
            String name = normalize(config.getName(), key);
            String icon = normalizeIcon(config.getIcon());
            String description = normalize(config.getDescription(), "external agent from " + fileName);
            List<String> tools = collectToolNames(config);
            List<String> skills = collectSkillNames(config);

            AgentMode agentMode = AgentModeFactory.create(mode, config, file, this::resolveModelByKey);
            RunSpec runSpec = agentMode.defaultRunSpec(config);

            return Optional.of(new AgentDefinition(
                    key,
                    name,
                    icon,
                    description,
                    primaryModel.key(),
                    primaryModel.provider(),
                    primaryModel.modelId(),
                    primaryModel.protocol(),
                    mode,
                    runSpec,
                    agentMode,
                    tools,
                    skills
            ));
        } catch (Exception ex) {
            log.warn("Skip invalid external agent file: {}", file, ex);
            return Optional.empty();
        }
    }

    private boolean isLegacyConfig(JsonNode root) {
        if (root == null || !root.isObject()) {
            return true;
        }
        if (root.has("deepThink")
                || root.has("systemPrompt")
                || root.has("providerKey")
                || root.has("model")
                || root.has("reasoning")
                || root.has("tools")) {
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
                || node.has("model")
                || node.has("reasoning")
                || node.has("tools");
    }

    private boolean hasRemovedFields(JsonNode root) {
        if (root == null || !root.isObject()) {
            return false;
        }
        if (hasLegacyModelConfigFields(root.path("modelConfig"))
                || hasLegacyModelConfigFields(root.path("plain").path("modelConfig"))
                || hasLegacyModelConfigFields(root.path("react").path("modelConfig"))
                || hasLegacyModelConfigFields(root.path("planExecute").path("plan").path("modelConfig"))
                || hasLegacyModelConfigFields(root.path("planExecute").path("execute").path("modelConfig"))
                || hasLegacyModelConfigFields(root.path("planExecute").path("summary").path("modelConfig"))) {
            return true;
        }
        if (root.has("verify") || root.has("output") || root.has("toolPolicy")) {
            return true;
        }
        JsonNode budget = root.path("budget");
        if (budget.isObject() && budget.has("maxSteps")) {
            return true;
        }
        JsonNode runtimePrompts = root.path("runtimePrompts");
        if (!runtimePrompts.isObject()) {
            return false;
        }
        if (runtimePrompts.has("verify")
                || runtimePrompts.has("finalAnswer")
                || runtimePrompts.has("oneshot")
                || runtimePrompts.has("react")) {
            return true;
        }
        JsonNode planExecute = runtimePrompts.path("planExecute");
        if (!planExecute.isObject()) {
            return false;
        }
        return planExecute.has("executeToolsTitle")
                || planExecute.has("planCallableToolsTitle")
                || planExecute.has("draftInstructionBlock")
                || planExecute.has("generateInstructionBlockFromDraft")
                || planExecute.has("generateInstructionBlockDirect")
                || planExecute.has("taskRequireToolUserPrompt")
                || planExecute.has("taskMultipleToolsUserPrompt")
                || planExecute.has("taskUpdateNoProgressUserPrompt")
                || planExecute.has("taskContinueUserPrompt")
                || planExecute.has("updateRoundPromptTemplate")
                || planExecute.has("updateRoundMultipleToolsUserPrompt")
                || planExecute.has("allStepsCompletedUserPrompt");
    }

    private boolean hasLegacyModelConfigFields(JsonNode modelConfig) {
        if (modelConfig == null || !modelConfig.isObject()) {
            return false;
        }
        return modelConfig.has("providerKey") || modelConfig.has("model");
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

    private boolean hasAnyModelConfig(AgentConfigFile config) {
        if (config == null) {
            return false;
        }
        if (hasModelKey(config.getModelConfig())) {
            return true;
        }
        if (config.getPlain() != null && hasModelKey(config.getPlain().getModelConfig())) {
            return true;
        }
        if (config.getReact() != null && hasModelKey(config.getReact().getModelConfig())) {
            return true;
        }
        if (config.getPlanExecute() == null) {
            return false;
        }
        return hasStageModelConfig(config.getPlanExecute().getPlan())
                || hasStageModelConfig(config.getPlanExecute().getExecute())
                || hasStageModelConfig(config.getPlanExecute().getSummary());
    }

    private boolean hasStageModelConfig(AgentConfigFile.StageConfig stageConfig) {
        return stageConfig != null && hasModelKey(stageConfig.getModelConfig());
    }

    private boolean hasModelKey(AgentConfigFile.ModelConfig modelConfig) {
        return modelConfig != null && StringUtils.hasText(modelConfig.getModelKey());
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

    private List<String> normalizeSkillNames(List<String> rawSkills) {
        if (rawSkills == null || rawSkills.isEmpty()) {
            return List.of();
        }
        List<String> skills = new ArrayList<>();
        for (String raw : rawSkills) {
            if (raw == null || raw.isBlank()) {
                continue;
            }
            skills.add(raw.trim().toLowerCase(Locale.ROOT));
        }
        return List.copyOf(skills);
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
        AgentRuntimeMode mode = config == null ? null : config.getMode();
        if (mode == AgentRuntimeMode.PLAN_EXECUTE) {
            merged.add(PLAN_ADD_TASK_TOOL);
            merged.add(PLAN_UPDATE_TASK_TOOL);
        }
        return merged.stream().distinct().toList();
    }

    private List<String> collectSkillNames(AgentConfigFile config) {
        if (config == null) {
            return List.of();
        }
        List<String> merged = new ArrayList<>();
        merged.addAll(normalizeSkillNames(config.getSkills()));
        if (config.getSkillConfig() != null) {
            merged.addAll(normalizeSkillNames(config.getSkillConfig().getSkills()));
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

    private Optional<ModelDefinition> resolvePrimaryModel(AgentConfigFile config) {
        String primaryKey = resolvePrimaryModelKey(config);
        if (!StringUtils.hasText(primaryKey)) {
            return Optional.empty();
        }
        ModelDefinition resolved = resolveModelByKey(primaryKey);
        return Optional.ofNullable(resolved);
    }

    private String resolvePrimaryModelKey(AgentConfigFile config) {
        if (config == null) {
            return null;
        }
        if (config.getModelConfig() != null && StringUtils.hasText(config.getModelConfig().getModelKey())) {
            return config.getModelConfig().getModelKey();
        }
        if (config.getPlain() != null && config.getPlain().getModelConfig() != null
                && StringUtils.hasText(config.getPlain().getModelConfig().getModelKey())) {
            return config.getPlain().getModelConfig().getModelKey();
        }
        if (config.getReact() != null && config.getReact().getModelConfig() != null
                && StringUtils.hasText(config.getReact().getModelConfig().getModelKey())) {
            return config.getReact().getModelConfig().getModelKey();
        }
        if (config.getPlanExecute() == null) {
            return null;
        }
        String fromPlan = resolveStageModelKey(config.getPlanExecute().getPlan());
        if (StringUtils.hasText(fromPlan)) {
            return fromPlan;
        }
        String fromExecute = resolveStageModelKey(config.getPlanExecute().getExecute());
        if (StringUtils.hasText(fromExecute)) {
            return fromExecute;
        }
        return resolveStageModelKey(config.getPlanExecute().getSummary());
    }

    private String resolveStageModelKey(AgentConfigFile.StageConfig stageConfig) {
        if (stageConfig == null || stageConfig.getModelConfig() == null) {
            return null;
        }
        return stageConfig.getModelConfig().getModelKey();
    }

    private ModelDefinition resolveModelByKey(String rawModelKey) {
        String modelKey = normalize(rawModelKey, "").trim().toLowerCase(Locale.ROOT);
        if (modelKey.isBlank()) {
            return null;
        }
        if (modelRegistryService != null) {
            return modelRegistryService.find(modelKey).orElse(null);
        }
        return fallbackModels().get(modelKey);
    }

    private Map<String, ModelDefinition> fallbackModels() {
        Map<String, ModelDefinition> map = new LinkedHashMap<>();
        map.put("bailian-qwen3-max", new ModelDefinition(
                "bailian-qwen3-max",
                "bailian",
                ModelProtocol.OPENAI,
                "qwen3-max",
                false,
                true,
                null,
                null,
                null,
                null,
                null
        ));
        map.put("bailian-qwen3_5-plus", new ModelDefinition(
                "bailian-qwen3_5-plus",
                "bailian",
                ModelProtocol.OPENAI,
                "qwen3.5-plus",
                true,
                true,
                null,
                null,
                null,
                null,
                null
        ));
        map.put("siliconflow-deepseek-v3_2", new ModelDefinition(
                "siliconflow-deepseek-v3_2",
                "siliconflow",
                ModelProtocol.OPENAI,
                "deepseek-ai/DeepSeek-V3.2",
                true,
                true,
                null,
                null,
                null,
                null,
                null
        ));
        return map;
    }
}
