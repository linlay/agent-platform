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

    private String description;
    private String providerKey;
    private String providerType;
    private String model;
    private AgentRuntimeMode mode;
    private List<String> tools;

    private ComputePolicy compute;
    private OutputPolicy output;
    private ToolPolicy toolPolicy;
    private VerifyPolicy verify;
    private BudgetConfig budget;

    private PlainConfig plain;
    private ThinkingConfig thinking;
    private PlainToolingConfig plainTooling;
    private ThinkingToolingConfig thinkingTooling;
    private ReactConfig react;
    private PlanExecuteConfig planExecute;

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

    public ComputePolicy getCompute() {
        return compute;
    }

    public void setCompute(ComputePolicy compute) {
        this.compute = compute;
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

    public PlainConfig getPlain() {
        return plain;
    }

    public void setPlain(PlainConfig plain) {
        this.plain = plain;
    }

    public ThinkingConfig getThinking() {
        return thinking;
    }

    public void setThinking(ThinkingConfig thinking) {
        this.thinking = thinking;
    }

    public PlainToolingConfig getPlainTooling() {
        return plainTooling;
    }

    public void setPlainTooling(PlainToolingConfig plainTooling) {
        this.plainTooling = plainTooling;
    }

    public ThinkingToolingConfig getThinkingTooling() {
        return thinkingTooling;
    }

    public void setThinkingTooling(ThinkingToolingConfig thinkingTooling) {
        this.thinkingTooling = thinkingTooling;
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
    public static class PlainConfig {
        private String systemPrompt;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ThinkingConfig {
        private String systemPrompt;
        private Boolean exposeReasoningToUser;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

        public Boolean getExposeReasoningToUser() {
            return exposeReasoningToUser;
        }

        public void setExposeReasoningToUser(Boolean exposeReasoningToUser) {
            this.exposeReasoningToUser = exposeReasoningToUser;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class PlainToolingConfig {
        private String systemPrompt;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ThinkingToolingConfig {
        private String systemPrompt;
        private Boolean exposeReasoningToUser;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

        public Boolean getExposeReasoningToUser() {
            return exposeReasoningToUser;
        }

        public void setExposeReasoningToUser(Boolean exposeReasoningToUser) {
            this.exposeReasoningToUser = exposeReasoningToUser;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ReactConfig {
        private String systemPrompt;
        private Integer maxSteps;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

        public Integer getMaxSteps() {
            return maxSteps;
        }

        public void setMaxSteps(Integer maxSteps) {
            this.maxSteps = maxSteps;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class PlanExecuteConfig {
        private String planSystemPrompt;
        private String executeSystemPrompt;
        private String summarySystemPrompt;

        public String getPlanSystemPrompt() {
            return planSystemPrompt;
        }

        public void setPlanSystemPrompt(String planSystemPrompt) {
            this.planSystemPrompt = planSystemPrompt;
        }

        public String getExecuteSystemPrompt() {
            return executeSystemPrompt;
        }

        public void setExecuteSystemPrompt(String executeSystemPrompt) {
            this.executeSystemPrompt = executeSystemPrompt;
        }

        public String getSummarySystemPrompt() {
            return summarySystemPrompt;
        }

        public void setSummarySystemPrompt(String summarySystemPrompt) {
            this.summarySystemPrompt = summarySystemPrompt;
        }
    }
}
