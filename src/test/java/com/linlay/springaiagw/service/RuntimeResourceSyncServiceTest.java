package com.linlay.springaiagw.service;

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
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);

        Path weatherTool = toolsDir.resolve("mock_city_weather.backend");
        Path weatherViewport = viewportsDir.resolve("show_weather_card.html");
        Path extraTool = toolsDir.resolve("custom.backend");
        Path extraViewport = viewportsDir.resolve("custom.html");

        Files.writeString(weatherTool, "old-tool-content");
        Files.writeString(weatherViewport, "old-viewport-content");
        Files.writeString(extraTool, "custom tool content");
        Files.writeString(extraViewport, "custom viewport content");

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                tempDir
        );
        service.syncRuntimeDirectories();
        service.syncRuntimeDirectories();

        String syncedTool = Files.readString(weatherTool);
        String syncedViewport = Files.readString(weatherViewport);

        assertThat(syncedTool).contains("\"name\": \"mock_city_weather\"");
        assertThat(syncedTool).contains("\"prompt\"");
        assertThat(syncedViewport).contains("<title>Weather Card</title>");
        assertThat(Files.readString(extraTool)).isEqualTo("custom tool content");
        assertThat(Files.readString(extraViewport)).isEqualTo("custom viewport content");
    }
}
