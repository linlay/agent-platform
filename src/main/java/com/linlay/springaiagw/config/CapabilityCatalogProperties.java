package com.linlay.springaiagw.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "agent.capability")
public class CapabilityCatalogProperties {

    private String toolsExternalDir = "tools";
    private long refreshIntervalMs = 30_000L;

    public String getToolsExternalDir() {
        return toolsExternalDir;
    }

    public void setToolsExternalDir(String toolsExternalDir) {
        this.toolsExternalDir = toolsExternalDir;
    }

    public long getRefreshIntervalMs() {
        return refreshIntervalMs;
    }

    public void setRefreshIntervalMs(long refreshIntervalMs) {
        this.refreshIntervalMs = refreshIntervalMs;
    }
}
