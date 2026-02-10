package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BashTool;
import com.linlay.springaiagw.tool.MockCityDateTimeTool;
import com.linlay.springaiagw.tool.MockCityWeatherTool;
import com.linlay.springaiagw.tool.MockOpsRunbookTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class AgentRegistryTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldUseCachedAgentsUntilRefresh() throws IOException {
        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);
        LlmService llmService = new LlmService(null, null);
        DeltaStreamService deltaStreamService = new DeltaStreamService();
        ToolRegistry toolRegistry = new ToolRegistry(List.of(
                new MockCityDateTimeTool(),
                new MockCityWeatherTool(),
                new MockOpsRunbookTool(),
                new BashTool()
        ));

        AgentRegistry registry = new AgentRegistry(loader, llmService, deltaStreamService, toolRegistry, new ObjectMapper());

        assertThat(registry.listIds()).contains("demoPlain", "demoReAct", "demoPlanExecute");
        assertThatThrownBy(() -> registry.get("demoExternal"))
                .isInstanceOf(IllegalArgumentException.class);

        Path dynamicFile = tempDir.resolve("demoExternal.json");
        Files.writeString(dynamicFile, """
                {
                  "description": "动态加载智能体",
                  "providerType": "BAILIAN",
                  "model": "qwen3-max",
                  "systemPrompt": "你是动态智能体",
                  "mode": "RE_ACT",
                  "tools": ["bash"]
                }
                """);

        assertThatThrownBy(() -> registry.get("demoExternal"))
                .isInstanceOf(IllegalArgumentException.class);

        registry.refreshAgents();
        Agent loaded = registry.get("demoExternal");
        assertThat(loaded.id()).isEqualTo("demoExternal");
        assertThat(registry.listIds()).contains("demoExternal");

        Files.delete(dynamicFile);
        assertThat(registry.listIds()).contains("demoExternal");

        registry.refreshAgents();
        assertThatThrownBy(() -> registry.get("demoExternal"))
                .isInstanceOf(IllegalArgumentException.class);
    }
}
