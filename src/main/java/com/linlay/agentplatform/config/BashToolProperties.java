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
}
