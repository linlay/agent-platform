package com.linlay.agentplatform.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "agent.chat-image-token")
public class ChatImageTokenProperties {

    private String secret;
    private String previousSecrets;
    private long ttlSeconds = 86_400L;

    public String getSecret() {
        return secret;
    }

    public void setSecret(String secret) {
        this.secret = secret;
    }

    public String getPreviousSecrets() {
        return previousSecrets;
    }

    public void setPreviousSecrets(String previousSecrets) {
        this.previousSecrets = previousSecrets;
    }

    public long getTtlSeconds() {
        return ttlSeconds;
    }

    public void setTtlSeconds(long ttlSeconds) {
        this.ttlSeconds = ttlSeconds;
    }
}
