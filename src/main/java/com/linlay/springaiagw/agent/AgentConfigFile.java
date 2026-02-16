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

    public RuntimePromptsConfig getRuntimePrompts() {
        return runtimePrompts;
    }

    public void setRuntimePrompts(RuntimePromptsConfig runtimePrompts) {
        this.runtimePrompts = runtimePrompts;
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

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class RuntimePromptsConfig {
        private VerifyPromptConfig verify;
        private FinalAnswerPromptConfig finalAnswer;
        private OneshotPromptConfig oneshot;
        private ReactPromptConfig react;
        private PlanExecutePromptConfig planExecute;
        private SkillPromptConfig skill;
        private ToolAppendixPromptConfig toolAppendix;

        public VerifyPromptConfig getVerify() {
            return verify;
        }

        public void setVerify(VerifyPromptConfig verify) {
            this.verify = verify;
        }

        public FinalAnswerPromptConfig getFinalAnswer() {
            return finalAnswer;
        }

        public void setFinalAnswer(FinalAnswerPromptConfig finalAnswer) {
            this.finalAnswer = finalAnswer;
        }

        public OneshotPromptConfig getOneshot() {
            return oneshot;
        }

        public void setOneshot(OneshotPromptConfig oneshot) {
            this.oneshot = oneshot;
        }

        public ReactPromptConfig getReact() {
            return react;
        }

        public void setReact(ReactPromptConfig react) {
            this.react = react;
        }

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
    public static class VerifyPromptConfig {
        private String systemPrompt;
        private String userPromptTemplate;

        public String getSystemPrompt() {
            return systemPrompt;
        }

        public void setSystemPrompt(String systemPrompt) {
            this.systemPrompt = systemPrompt;
        }

        public String getUserPromptTemplate() {
            return userPromptTemplate;
        }

        public void setUserPromptTemplate(String userPromptTemplate) {
            this.userPromptTemplate = userPromptTemplate;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class FinalAnswerPromptConfig {
        private String forceFinalUserPrompt;
        private String blockedAnswerTemplate;

        public String getForceFinalUserPrompt() {
            return forceFinalUserPrompt;
        }

        public void setForceFinalUserPrompt(String forceFinalUserPrompt) {
            this.forceFinalUserPrompt = forceFinalUserPrompt;
        }

        public String getBlockedAnswerTemplate() {
            return blockedAnswerTemplate;
        }

        public void setBlockedAnswerTemplate(String blockedAnswerTemplate) {
            this.blockedAnswerTemplate = blockedAnswerTemplate;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class OneshotPromptConfig {
        private String requireToolUserPrompt;
        private String finalAnswerUserPrompt;

        public String getRequireToolUserPrompt() {
            return requireToolUserPrompt;
        }

        public void setRequireToolUserPrompt(String requireToolUserPrompt) {
            this.requireToolUserPrompt = requireToolUserPrompt;
        }

        public String getFinalAnswerUserPrompt() {
            return finalAnswerUserPrompt;
        }

        public void setFinalAnswerUserPrompt(String finalAnswerUserPrompt) {
            this.finalAnswerUserPrompt = finalAnswerUserPrompt;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class ReactPromptConfig {
        private String requireToolUserPrompt;

        public String getRequireToolUserPrompt() {
            return requireToolUserPrompt;
        }

        public void setRequireToolUserPrompt(String requireToolUserPrompt) {
            this.requireToolUserPrompt = requireToolUserPrompt;
        }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class PlanExecutePromptConfig {
        private String executeToolsTitle;
        private String planCallableToolsTitle;
        private String draftInstructionBlock;
        private String generateInstructionBlockFromDraft;
        private String generateInstructionBlockDirect;
        private String taskExecutionPromptTemplate;
        private String taskRequireToolUserPrompt;
        private String taskMultipleToolsUserPrompt;
        private String taskUpdateNoProgressUserPrompt;
        private String taskContinueUserPrompt;
        private String updateRoundPromptTemplate;
        private String updateRoundMultipleToolsUserPrompt;
        private String allStepsCompletedUserPrompt;

        public String getExecuteToolsTitle() {
            return executeToolsTitle;
        }

        public void setExecuteToolsTitle(String executeToolsTitle) {
            this.executeToolsTitle = executeToolsTitle;
        }

        public String getPlanCallableToolsTitle() {
            return planCallableToolsTitle;
        }

        public void setPlanCallableToolsTitle(String planCallableToolsTitle) {
            this.planCallableToolsTitle = planCallableToolsTitle;
        }

        public String getDraftInstructionBlock() {
            return draftInstructionBlock;
        }

        public void setDraftInstructionBlock(String draftInstructionBlock) {
            this.draftInstructionBlock = draftInstructionBlock;
        }

        public String getGenerateInstructionBlockFromDraft() {
            return generateInstructionBlockFromDraft;
        }

        public void setGenerateInstructionBlockFromDraft(String generateInstructionBlockFromDraft) {
            this.generateInstructionBlockFromDraft = generateInstructionBlockFromDraft;
        }

        public String getGenerateInstructionBlockDirect() {
            return generateInstructionBlockDirect;
        }

        public void setGenerateInstructionBlockDirect(String generateInstructionBlockDirect) {
            this.generateInstructionBlockDirect = generateInstructionBlockDirect;
        }

        public String getTaskExecutionPromptTemplate() {
            return taskExecutionPromptTemplate;
        }

        public void setTaskExecutionPromptTemplate(String taskExecutionPromptTemplate) {
            this.taskExecutionPromptTemplate = taskExecutionPromptTemplate;
        }

        public String getTaskRequireToolUserPrompt() {
            return taskRequireToolUserPrompt;
        }

        public void setTaskRequireToolUserPrompt(String taskRequireToolUserPrompt) {
            this.taskRequireToolUserPrompt = taskRequireToolUserPrompt;
        }

        public String getTaskMultipleToolsUserPrompt() {
            return taskMultipleToolsUserPrompt;
        }

        public void setTaskMultipleToolsUserPrompt(String taskMultipleToolsUserPrompt) {
            this.taskMultipleToolsUserPrompt = taskMultipleToolsUserPrompt;
        }

        public String getTaskUpdateNoProgressUserPrompt() {
            return taskUpdateNoProgressUserPrompt;
        }

        public void setTaskUpdateNoProgressUserPrompt(String taskUpdateNoProgressUserPrompt) {
            this.taskUpdateNoProgressUserPrompt = taskUpdateNoProgressUserPrompt;
        }

        public String getTaskContinueUserPrompt() {
            return taskContinueUserPrompt;
        }

        public void setTaskContinueUserPrompt(String taskContinueUserPrompt) {
            this.taskContinueUserPrompt = taskContinueUserPrompt;
        }

        public String getUpdateRoundPromptTemplate() {
            return updateRoundPromptTemplate;
        }

        public void setUpdateRoundPromptTemplate(String updateRoundPromptTemplate) {
            this.updateRoundPromptTemplate = updateRoundPromptTemplate;
        }

        public String getUpdateRoundMultipleToolsUserPrompt() {
            return updateRoundMultipleToolsUserPrompt;
        }

        public void setUpdateRoundMultipleToolsUserPrompt(String updateRoundMultipleToolsUserPrompt) {
            this.updateRoundMultipleToolsUserPrompt = updateRoundMultipleToolsUserPrompt;
        }

        public String getAllStepsCompletedUserPrompt() {
            return allStepsCompletedUserPrompt;
        }

        public void setAllStepsCompletedUserPrompt(String allStepsCompletedUserPrompt) {
            this.allStepsCompletedUserPrompt = allStepsCompletedUserPrompt;
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
