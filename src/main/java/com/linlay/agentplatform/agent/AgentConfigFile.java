package com.linlay.agentplatform.agent;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonAnySetter;
import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonSetter;
import com.linlay.agentplatform.agent.runtime.AgentRuntimeMode;
import com.linlay.agentplatform.agent.runtime.policy.Budget;
import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.agent.runtime.policy.ToolChoice;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

@JsonIgnoreProperties(ignoreUnknown = true)
public class AgentConfigFile {

    private String key;
    private String name;
    private String icon;
    private String description;
    private ModelConfig modelConfig;
    private ToolConfig toolConfig;
    private SkillConfig skillConfig;
    private List<String> skills;
    private AgentRuntimeMode mode;

    private ToolChoice toolChoice;
    private BudgetConfig budget;

    private OneshotConfig plain;
    private ReactConfig react;
    private PlanExecuteConfig planExecute;
    private RuntimePromptsConfig runtimePrompts;

    public String getKey() {
        return key;
    }

    public void setKey(String key) {
        this.key = key;
    }

    public String getName() {
        return name;
    }

    public void setName(String name) {
        this.name = name;
    }

    public String getIcon() {
        return icon;
    }

    public void setIcon(String icon) {
        this.icon = icon;
    }

    public String getDescription() {
        return description;
    }

    public void setDescription(String description) {
        this.description = description;
    }

    public ModelConfig getModelConfig() {
        return modelConfig;
    }

    public void setModelConfig(ModelConfig modelConfig) {
        this.modelConfig = modelConfig;
    }

    public ToolConfig getToolConfig() {
        return toolConfig;
    }

    public void setToolConfig(ToolConfig toolConfig) {
        this.toolConfig = toolConfig;
    }

    public SkillConfig getSkillConfig() {
        return skillConfig;
    }

    public void setSkillConfig(SkillConfig skillConfig) {
        this.skillConfig = skillConfig;
    }

    public List<String> getSkills() {
        return skills;
    }

    public void setSkills(List<String> skills) {
        this.skills = skills;
    }

    public AgentRuntimeMode getMode() {
        return mode;
    }

    public void setMode(AgentRuntimeMode mode) {
        this.mode = mode;
    }

    public ToolChoice getToolChoice() {
        return toolChoice;
    }

    public void setToolChoice(ToolChoice toolChoice) {
        this.toolChoice = toolChoice;
    }

    public BudgetConfig getBudget() {
        return budget;
    }

    public void setBudget(BudgetConfig budget) {
        this.budget = budget;
    }

    public OneshotConfig getPlain() {
        return plain;
    }

    public void setPlain(OneshotConfig plain) {
        this.plain = plain;
    }

    public ReactConfig getReact() {
        return react;
    }

    public void setReact(ReactConfig react) {
        this.react = react;
    }

    public PlanExecuteConfig getPlanExecute() {
        return planExecute;
    }

    public void setPlanExecute(PlanExecuteConfig planExecute) {
        this.planExecute = planExecute;
    }

    public RuntimePromptsConfig getRuntimePrompts() {
        return runtimePrompts;
    }

    public void setRuntimePrompts(RuntimePromptsConfig runtimePrompts) {
        this.runtimePrompts = runtimePrompts;
    }

    public static class BudgetConfig {
        private static final Set<String> LEGACY_FIELDS = Set.of(
                "maxModelCalls",
                "maxToolCalls",
                "timeoutMs",
                "retryCount"
        );

        private Long runTimeoutMs;
        private ScopeConfig model;
        private ScopeConfig tool;
        private final Map<String, Object> unknownFields = new LinkedHashMap<>();

        public Long getRunTimeoutMs() {
            return runTimeoutMs;
        }

        public void setRunTimeoutMs(Long runTimeoutMs) {
            this.runTimeoutMs = runTimeoutMs;
        }

        public ScopeConfig getModel() {
            return model;
        }

        public void setModel(ScopeConfig model) {
            this.model = model;
        }

        public ScopeConfig getTool() {
            return tool;
        }

        public void setTool(ScopeConfig tool) {
            this.tool = tool;
        }

        @JsonAnySetter
        public void setUnknownField(String key, Object value) {
            if (key == null || key.isBlank()) {
                return;
            }
            unknownFields.put(key, value);
        }

        public Map<String, Object> getUnknownFields() {
            return Map.copyOf(unknownFields);
        }

        public Budget toBudget() {
            if (!unknownFields.isEmpty()) {
                List<String> names = unknownFields.keySet().stream().sorted().toList();
                List<String> legacy = names.stream().filter(LEGACY_FIELDS::contains).toList();
                if (!legacy.isEmpty()) {
                    throw new IllegalArgumentException(
                            "budget legacy fields are not supported: " + String.join(", ", legacy)
                    );
                }
                throw new IllegalArgumentException(
                        "budget contains unsupported fields: " + String.join(", ", names)
                );
            }
            return new Budget(
                    runTimeoutMs == null ? 0L : runTimeoutMs,
                    model == null ? null : model.toScope(),
                    tool == null ? null : tool.toScope()
            );
        }

        @JsonIgnoreProperties(ignoreUnknown = true)
        public static class ScopeConfig {
            private Integer maxCalls;
            private Long timeoutMs;
            private Integer retryCount;

            public Integer getMaxCalls() {
                return maxCalls;
            }

            public void setMaxCalls(Integer maxCalls) {
                this.maxCalls = maxCalls;
            }

            public Long getTimeoutMs() {
                return timeoutMs;
            }

            public void setTimeoutMs(Long timeoutMs) {
                this.timeoutMs = timeoutMs;
            }

            public Integer getRetryCount() {
                return retryCount;
            }

            public void setRetryCount(Integer retryCount) {
                this.retryCount = retryCount;
            }

            private Budget.Scope toScope() {
                return new Budget.Scope(
                        maxCalls == null ? 0 : maxCalls,
                        timeoutMs == null ? 0L : timeoutMs,
                        retryCount == null ? 0 : retryCount
                );
            }
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ReasoningConfig {
        private Boolean enabled;
        private ComputePolicy effort;

        public Boolean getEnabled() {
            return enabled;
        }

        public void setEnabled(Boolean enabled) {
            this.enabled = enabled;
        }

        public ComputePolicy getEffort() {
            return effort;
        }

        public void setEffort(ComputePolicy effort) {
            this.effort = effort;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ModelConfig {
        private String modelKey;
        private ReasoningConfig reasoning;
        private Double temperature;
        @JsonProperty("top_p")
        private Double topP;
        @JsonProperty("max_tokens")
        private Integer maxTokens;

        public String getModelKey() {
            return modelKey;
        }

        public void setModelKey(String modelKey) {
            this.modelKey = modelKey;
        }

        public ReasoningConfig getReasoning() {
            return reasoning;
        }

        public void setReasoning(ReasoningConfig reasoning) {
            this.reasoning = reasoning;
        }

        public Double getTemperature() {
            return temperature;
        }

        public void setTemperature(Double temperature) {
            this.temperature = temperature;
        }

        public Double getTopP() {
            return topP;
        }

        public void setTopP(Double topP) {
            this.topP = topP;
        }

        public Integer getMaxTokens() {
            return maxTokens;
        }

        public void setMaxTokens(Integer maxTokens) {
            this.maxTokens = maxTokens;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ToolConfig {
        private List<String> backends;
        private List<String> frontends;
        private List<String> actions;

        public List<String> getBackends() {
            return backends;
        }

        public void setBackends(List<String> backends) {
            this.backends = backends;
        }

        public List<String> getFrontends() {
            return frontends;
        }

        public void setFrontends(List<String> frontends) {
            this.frontends = frontends;
        }

        public List<String> getActions() {
            return actions;
        }

        public void setActions(List<String> actions) {
            this.actions = actions;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class SkillConfig {
        private List<String> skills;

        public List<String> getSkills() {
            return skills;
        }

        public void setSkills(List<String> skills) {
            this.skills = skills;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class StageConfig {
        private String systemPrompt;
        private boolean deepThinking;
        private ModelConfig modelConfig;
        private ToolConfig toolConfig;
        @JsonIgnore
        private boolean deepThinkingProvided;
        @JsonIgnore
        private boolean modelConfigProvided;
        @JsonIgnore
        private boolean toolConfigProvided;
        @JsonIgnore
        private boolean toolConfigExplicitNull;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

        public boolean isDeepThinking() {
            return deepThinking;
        }

        @JsonSetter("deepThinking")
        public void setDeepThinking(boolean deepThinking) {
            this.deepThinking = deepThinking;
            this.deepThinkingProvided = true;
        }

        public ModelConfig getModelConfig() {
            return modelConfig;
        }

        @JsonSetter("modelConfig")
        public void setModelConfig(ModelConfig modelConfig) {
            this.modelConfig = modelConfig;
            this.modelConfigProvided = true;
        }

        public ToolConfig getToolConfig() {
            return toolConfig;
        }

        @JsonSetter("toolConfig")
        public void setToolConfig(ToolConfig toolConfig) {
            this.toolConfig = toolConfig;
            this.toolConfigProvided = true;
            this.toolConfigExplicitNull = toolConfig == null;
        }

        public boolean isModelConfigProvided() {
            return modelConfigProvided;
        }

        public boolean isToolConfigProvided() {
            return toolConfigProvided;
        }

        public boolean isToolConfigExplicitNull() {
            return toolConfigExplicitNull;
        }

        public boolean isDeepThinkingProvided() {
            return deepThinkingProvided;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class OneshotConfig extends StageConfig {
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ReactConfig extends StageConfig {
        private Integer maxSteps;

        public Integer getMaxSteps() {
            return maxSteps;
        }

        public void setMaxSteps(Integer maxSteps) {
            this.maxSteps = maxSteps;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class PlanExecuteConfig {
        private StageConfig plan;
        private StageConfig execute;
        private StageConfig summary;
        private Integer maxSteps;

        public StageConfig getPlan() {
            return plan;
        }

        public void setPlan(StageConfig plan) {
            this.plan = plan;
        }

        public StageConfig getExecute() {
            return execute;
        }

        public void setExecute(StageConfig execute) {
            this.execute = execute;
        }

        public StageConfig getSummary() {
            return summary;
        }

        public void setSummary(StageConfig summary) {
            this.summary = summary;
        }

        public Integer getMaxSteps() {
            return maxSteps;
        }

        public void setMaxSteps(Integer maxSteps) {
            this.maxSteps = maxSteps;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class RuntimePromptsConfig {
        private PlanExecutePromptConfig planExecute;
        private SkillPromptConfig skill;
        private ToolAppendixPromptConfig toolAppendix;

        public PlanExecutePromptConfig getPlanExecute() {
            return planExecute;
        }

        public void setPlanExecute(PlanExecutePromptConfig planExecute) {
            this.planExecute = planExecute;
        }

        public SkillPromptConfig getSkill() {
            return skill;
        }

        public void setSkill(SkillPromptConfig skill) {
            this.skill = skill;
        }

        public ToolAppendixPromptConfig getToolAppendix() {
            return toolAppendix;
        }

        public void setToolAppendix(ToolAppendixPromptConfig toolAppendix) {
            this.toolAppendix = toolAppendix;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class PlanExecutePromptConfig {
        private String taskExecutionPromptTemplate;

        public String getTaskExecutionPromptTemplate() {
            return taskExecutionPromptTemplate;
        }

        public void setTaskExecutionPromptTemplate(String taskExecutionPromptTemplate) {
            this.taskExecutionPromptTemplate = taskExecutionPromptTemplate;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class SkillPromptConfig {
        private String catalogHeader;
        private String disclosureHeader;
        private String instructionsLabel;

        public String getCatalogHeader() {
            return catalogHeader;
        }

        public void setCatalogHeader(String catalogHeader) {
            this.catalogHeader = catalogHeader;
        }

        public String getDisclosureHeader() {
            return disclosureHeader;
        }

        public void setDisclosureHeader(String disclosureHeader) {
            this.disclosureHeader = disclosureHeader;
        }

        public String getInstructionsLabel() {
            return instructionsLabel;
        }

        public void setInstructionsLabel(String instructionsLabel) {
            this.instructionsLabel = instructionsLabel;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ToolAppendixPromptConfig {
        private String toolDescriptionTitle;
        private String afterCallHintTitle;

        public String getToolDescriptionTitle() {
            return toolDescriptionTitle;
        }

        public void setToolDescriptionTitle(String toolDescriptionTitle) {
            this.toolDescriptionTitle = toolDescriptionTitle;
        }

        public String getAfterCallHintTitle() {
            return afterCallHintTitle;
        }

        public void setAfterCallHintTitle(String afterCallHintTitle) {
            this.afterCallHintTitle = afterCallHintTitle;
        }
    }
}
