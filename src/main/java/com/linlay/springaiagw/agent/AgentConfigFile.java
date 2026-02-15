package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonSetter;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;

import java.util.List;

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

    private OutputPolicy output;
    private ToolPolicy toolPolicy;
    private VerifyPolicy verify;
    private BudgetConfig budget;

    private OneshotConfig plain;
    private ReactConfig react;
    private PlanExecuteConfig planExecute;

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

    public OutputPolicy getOutput() {
        return output;
    }

    public void setOutput(OutputPolicy output) {
        this.output = output;
    }

    public ToolPolicy getToolPolicy() {
        return toolPolicy;
    }

    public void setToolPolicy(ToolPolicy toolPolicy) {
        this.toolPolicy = toolPolicy;
    }

    public VerifyPolicy getVerify() {
        return verify;
    }

    public void setVerify(VerifyPolicy verify) {
        this.verify = verify;
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

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class BudgetConfig {
        private Integer maxModelCalls;
        private Integer maxToolCalls;
        private Integer maxSteps;
        private Long timeoutMs;

        public Integer getMaxModelCalls() {
            return maxModelCalls;
        }

        public void setMaxModelCalls(Integer maxModelCalls) {
            this.maxModelCalls = maxModelCalls;
        }

        public Integer getMaxToolCalls() {
            return maxToolCalls;
        }

        public void setMaxToolCalls(Integer maxToolCalls) {
            this.maxToolCalls = maxToolCalls;
        }

        public Integer getMaxSteps() {
            return maxSteps;
        }

        public void setMaxSteps(Integer maxSteps) {
            this.maxSteps = maxSteps;
        }

        public Long getTimeoutMs() {
            return timeoutMs;
        }

        public void setTimeoutMs(Long timeoutMs) {
            this.timeoutMs = timeoutMs;
        }

        public Budget toBudget() {
            return new Budget(
                    maxModelCalls == null ? 0 : maxModelCalls,
                    maxToolCalls == null ? 0 : maxToolCalls,
                    maxSteps == null ? 0 : maxSteps,
                    timeoutMs == null ? 0L : timeoutMs
            );
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
        private String providerKey;
        private String model;
        private ReasoningConfig reasoning;
        private Double temperature;
        @JsonProperty("top_p")
        private Double topP;
        @JsonProperty("max_tokens")
        private Integer maxTokens;

        public String getProviderKey() {
            return providerKey;
        }

        public void setProviderKey(String providerKey) {
            this.providerKey = providerKey;
        }

        public String getModel() {
            return model;
        }

        public void setModel(String model) {
            this.model = model;
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
    }
}
