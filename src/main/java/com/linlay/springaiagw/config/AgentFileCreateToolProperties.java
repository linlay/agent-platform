package com.linlay.springaiagw.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "agent.tools.agent-file-create")
public class AgentFileCreateToolProperties {

    public static final String DEFAULT_SYSTEM_PROMPT = "你是通用助理，回答要清晰和可执行。";

    private String defaultSystemPrompt = DEFAULT_SYSTEM_PROMPT;

    public String getDefaultSystemPrompt() {
        return defaultSystemPrompt;
    }

    public void setDefaultSystemPrompt(String defaultSystemPrompt) {
        this.defaultSystemPrompt = defaultSystemPrompt;
    }
}
