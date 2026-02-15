package com.linlay.springaiagw.service;

import com.linlay.springaiagw.agent.AgentCatalogProperties;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
import com.linlay.springaiagw.config.ViewportCatalogProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.core.io.support.PathMatchingResourcePatternResolver;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;

class RuntimeResourceSyncServiceTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldOverwriteBuiltInResourcesAndKeepExtraFiles() throws Exception {
        Path agentsDir = tempDir.resolve("agents");
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Files.createDirectories(agentsDir);
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);

        Path modePlainAgent = agentsDir.resolve("demoModePlain.json");
        Path weatherTool = toolsDir.resolve("mock_city_weather.backend");
        Path weatherViewport = viewportsDir.resolve("show_weather_card.html");
        Path extraAgent = agentsDir.resolve("custom_agent.json");
        Path extraTool = toolsDir.resolve("custom.backend");
        Path extraViewport = viewportsDir.resolve("custom.html");

        Files.writeString(modePlainAgent, "old-agent-content");
        Files.writeString(weatherTool, "old-tool-content");
        Files.writeString(weatherViewport, "old-viewport-content");
        Files.writeString(extraAgent, "custom agent content");
        Files.writeString(extraTool, "custom tool content");
        Files.writeString(extraViewport, "custom viewport content");

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                agentsDir,
                viewportsDir,
                toolsDir
        );
        service.syncRuntimeDirectories();
        service.syncRuntimeDirectories();

        String syncedTool = Files.readString(weatherTool);
        String syncedViewport = Files.readString(weatherViewport);
        String syncedAgent = Files.readString(modePlainAgent);

        assertThat(syncedAgent).contains("\"mode\"").contains("\"ONESHOT\"");
        assertThat(syncedTool).contains("\"name\": \"mock_city_weather\"");
        assertThat(syncedTool).contains("\"prompt\"");
        assertThat(syncedViewport).contains("<title>Weather Card</title>");
        assertThat(Files.readString(extraAgent)).isEqualTo("custom agent content");
        assertThat(Files.readString(extraTool)).isEqualTo("custom tool content");
        assertThat(Files.readString(extraViewport)).isEqualTo("custom viewport content");
    }

    @Test
    void shouldSyncToConfiguredDirectoriesInsteadOfUserDirRoot() throws Exception {
        Path configuredAgentsDir = tempDir.resolve("configured").resolve("agents");
        Path configuredViewportsDir = tempDir.resolve("configured").resolve("viewports");
        Path configuredToolsDir = tempDir.resolve("configured").resolve("tools");
        Path legacyUserDir = tempDir.resolve("legacy-user-dir");

        AgentCatalogProperties agentProperties = new AgentCatalogProperties();
        agentProperties.setExternalDir(configuredAgentsDir.toString());
        ViewportCatalogProperties viewportProperties = new ViewportCatalogProperties();
        viewportProperties.setExternalDir(configuredViewportsDir.toString());
        CapabilityCatalogProperties capabilityProperties = new CapabilityCatalogProperties();
        capabilityProperties.setToolsExternalDir(configuredToolsDir.toString());

        String originalUserDir = System.getProperty("user.dir");
        try {
            System.setProperty("user.dir", legacyUserDir.toString());
            RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                    agentProperties,
                    viewportProperties,
                    capabilityProperties
            );
            service.syncRuntimeDirectories();
        } finally {
            if (originalUserDir == null) {
                System.clearProperty("user.dir");
            } else {
                System.setProperty("user.dir", originalUserDir);
            }
        }

        assertThat(configuredAgentsDir.resolve("demoModePlain.json")).exists();
        assertThat(configuredViewportsDir.resolve("show_weather_card.html")).exists();
        assertThat(configuredToolsDir.resolve("mock_city_weather.backend")).exists();
        assertThat(legacyUserDir.resolve("agents")).doesNotExist();
        assertThat(legacyUserDir.resolve("viewports")).doesNotExist();
        assertThat(legacyUserDir.resolve("tools")).doesNotExist();
    }

    @Test
    void shouldRemoveLegacyBashAliasWhenCanonicalToolExists() throws Exception {
        Path agentsDir = tempDir.resolve("agents");
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Files.createDirectories(agentsDir);
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);

        Path legacyBash = toolsDir.resolve("bash.backend");
        Files.writeString(legacyBash, """
                {
                  "tools": [
                    {
                      "type": "function",
                      "name": "_bash_",
                      "description": "legacy alias"
                    }
                  ]
                }
                """);

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                agentsDir,
                viewportsDir,
                toolsDir
        );
        service.syncRuntimeDirectories();

        assertThat(toolsDir.resolve("_bash_.backend")).exists();
        assertThat(legacyBash).doesNotExist();
    }
}
