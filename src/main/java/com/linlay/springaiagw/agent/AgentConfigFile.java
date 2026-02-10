package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.linlay.springaiagw.model.ProviderType;

import java.util.List;

@JsonIgnoreProperties(ignoreUnknown = true)
public class AgentConfigFile {

    private String description;
    private ProviderType providerType;
    private String model;
    private String systemPrompt;
    private Boolean deepThink;
    private AgentMode mode;
    private List<String> tools;

    public String getDescription() {
        return description;
    }

    public void setDescription(String description) {
        this.description = description;
    }

    public ProviderType getProviderType() {
        return providerType;
    }

    public void setProviderType(ProviderType providerType) {
        this.providerType = providerType;
    }

    public String getModel() {
        return model;
    }

    public void setModel(String model) {
        this.model = model;
    }

    public String getSystemPrompt() {
        return systemPrompt;
    }

    public void setSystemPrompt(String systemPrompt) {
        this.systemPrompt = systemPrompt;
    }

    public Boolean getDeepThink() {
        return deepThink;
    }

    public void setDeepThink(Boolean deepThink) {
        this.deepThink = deepThink;
    }

    public AgentMode getMode() {
        return mode;
    }

    public void setMode(AgentMode mode) {
        this.mode = mode;
    }

    public List<String> getTools() {
        return tools;
    }

    public void setTools(List<String> tools) {
        this.tools = tools;
    }
}
