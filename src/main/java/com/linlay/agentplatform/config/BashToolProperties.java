package com.linlay.agentplatform.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

import java.util.ArrayList;
import java.util.List;

@ConfigurationProperties(prefix = "agent.tools.bash")
public class BashToolProperties {

    private String workingDirectory = System.getProperty("user.dir", ".");
    private List<String> allowedPaths = new ArrayList<>();
    private List<String> allowedCommands = new ArrayList<>();
    private List<String> pathCheckedCommands = new ArrayList<>();
    private boolean shellFeaturesEnabled = false;
    private String shellExecutable = "bash";
    private int shellTimeoutMs = 10_000;
    private int maxCommandChars = 16_000;

    public String getWorkingDirectory() {
        return workingDirectory;
    }

    public void setWorkingDirectory(String workingDirectory) {
        this.workingDirectory = workingDirectory;
    }

    public List<String> getAllowedPaths() {
        return allowedPaths;
    }

    public void setAllowedPaths(List<String> allowedPaths) {
        this.allowedPaths = allowedPaths;
    }

    public List<String> getAllowedCommands() {
        return allowedCommands;
    }

    public void setAllowedCommands(List<String> allowedCommands) {
        this.allowedCommands = allowedCommands;
    }

    public List<String> getPathCheckedCommands() {
        return pathCheckedCommands;
    }

    public void setPathCheckedCommands(List<String> pathCheckedCommands) {
        this.pathCheckedCommands = pathCheckedCommands;
    }

    public boolean isShellFeaturesEnabled() {
        return shellFeaturesEnabled;
    }

    public void setShellFeaturesEnabled(boolean shellFeaturesEnabled) {
        this.shellFeaturesEnabled = shellFeaturesEnabled;
    }

    public String getShellExecutable() {
        return shellExecutable;
    }

    public void setShellExecutable(String shellExecutable) {
        this.shellExecutable = shellExecutable;
    }

    public int getShellTimeoutMs() {
        return shellTimeoutMs;
    }

    public void setShellTimeoutMs(int shellTimeoutMs) {
        this.shellTimeoutMs = shellTimeoutMs;
    }

    public int getMaxCommandChars() {
        return maxCommandChars;
    }

    public void setMaxCommandChars(int maxCommandChars) {
        this.maxCommandChars = maxCommandChars;
    }
}
