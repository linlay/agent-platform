package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
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
    private String providerKey;
    private String providerType;
    private String model;
    private AgentRuntimeMode mode;
    private List<String> tools;

    private ReasoningConfig reasoning;
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

    public String getProviderKey() {
        return providerKey;
    }

    public void setProviderKey(String providerKey) {
        this.providerKey = providerKey;
    }

    public String getProviderType() {
        return providerType;
    }

    public void setProviderType(String providerType) {
        this.providerType = providerType;
    }

    public String getModel() {
        return model;
    }

    public void setModel(String model) {
        this.model = model;
    }

    public AgentRuntimeMode getMode() {
        return mode;
    }

    public void setMode(AgentRuntimeMode mode) {
        this.mode = mode;
    }

    public List<String> getTools() {
        return tools;
    }

    public void setTools(List<String> tools) {
        this.tools = tools;
    }

    public ReasoningConfig getReasoning() {
        return reasoning;
    }

    public void setReasoning(ReasoningConfig reasoning) {
        this.reasoning = reasoning;
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
    public static class StageConfig {
        private String systemPrompt;
        private String providerKey;
        private String model;
        private List<String> tools;
        private ReasoningConfig reasoning;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

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

        public List<String> getTools() {
            return tools;
        }

        public void setTools(List<String> tools) {
            this.tools = tools;
        }

        public ReasoningConfig getReasoning() {
            return reasoning;
        }

        public void setReasoning(ReasoningConfig reasoning) {
            this.reasoning = reasoning;
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
