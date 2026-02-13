package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;

class CapabilityRegistryServiceTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldLoadBackendAndActionCapabilitiesFromToolsDirectory() throws Exception {
        Path toolsDir = tempDir.resolve("tools");
        Files.createDirectories(toolsDir);

        Files.writeString(toolsDir.resolve("bash.backend"), """
                {
                  "tools": [
                    {"type":"function", "name":"bash", "description":"bash tool", "prompt":"bash prompt", "parameters":{"type":"object"}}
                  ]
                }
                """);
        Files.writeString(toolsDir.resolve("switch_theme.action"), """
                {
                  "tools": [
                    {"type":"function", "name":"switch_theme", "description":"switch", "parameters":{"type":"object"}}
                  ]
                }
                """);
        Files.writeString(toolsDir.resolve("launch_fireworks.action"), """
                {
                  "tools": [
                    {"type":"function", "name":"launch_fireworks", "description":"fireworks", "parameters":{"type":"object"}}
                  ]
                }
                """);
        Files.writeString(toolsDir.resolve("show_modal.action"), """
                {
                  "tools": [
                    {"type":"function", "name":"show_modal", "description":"modal", "parameters":{"type":"object"}}
                  ]
                }
                """);

        CapabilityCatalogProperties properties = new CapabilityCatalogProperties();
        properties.setToolsExternalDir(toolsDir.toString());

        CapabilityRegistryService service = new CapabilityRegistryService(new ObjectMapper(), properties);

        CapabilityDescriptor backend = service.find("bash").orElseThrow();
        CapabilityDescriptor action = service.find("switch_theme").orElseThrow();
        CapabilityDescriptor fireworks = service.find("launch_fireworks").orElseThrow();
        CapabilityDescriptor modal = service.find("show_modal").orElseThrow();

        assertThat(backend.kind()).isEqualTo(CapabilityKind.BACKEND);
        assertThat(backend.toolType()).isEqualTo("function");
        assertThat(backend.prompt()).isEqualTo("bash prompt");
        assertThat(service.find("show_weather_card")).isEmpty();

        assertThat(action.kind()).isEqualTo(CapabilityKind.ACTION);
        assertThat(action.toolType()).isEqualTo("action");
        assertThat(fireworks.kind()).isEqualTo(CapabilityKind.ACTION);
        assertThat(fireworks.toolType()).isEqualTo("action");
        assertThat(modal.kind()).isEqualTo(CapabilityKind.ACTION);
        assertThat(modal.toolType()).isEqualTo("action");
    }

    @Test
    void shouldSkipConflictedCapabilityNames() throws Exception {
        Path toolsDir = tempDir.resolve("tools");
        Files.createDirectories(toolsDir);

        Files.writeString(toolsDir.resolve("a.backend"), """
                {
                  "tools": [
                    {"type":"function", "name":"dup_name", "description":"a", "parameters":{"type":"object"}}
                  ]
                }
                """);
        Files.writeString(toolsDir.resolve("b.action"), """
                {
                  "tools": [
                    {"type":"function", "name":"dup_name", "description":"b", "parameters":{"type":"object"}}
                  ]
                }
                """);

        CapabilityCatalogProperties properties = new CapabilityCatalogProperties();
        properties.setToolsExternalDir(toolsDir.toString());

        CapabilityRegistryService service = new CapabilityRegistryService(new ObjectMapper(), properties);

        assertThat(service.find("dup_name")).isEmpty();
    }
}
