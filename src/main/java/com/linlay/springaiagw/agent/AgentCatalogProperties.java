package com.linlay.springaiagw.agent;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "agent.catalog")
public class AgentCatalogProperties {

    private String externalDir = "agents";
    private long refreshIntervalMs = 10_000L;

    public String getExternalDir() {
        return externalDir;
    }

    public void setExternalDir(String externalDir) {
        this.externalDir = externalDir;
    }

    public long getRefreshIntervalMs() {
        return refreshIntervalMs;
    }

    public void setRefreshIntervalMs(long refreshIntervalMs) {
        this.refreshIntervalMs = refreshIntervalMs;
    }
}
