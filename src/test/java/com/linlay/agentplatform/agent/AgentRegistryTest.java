package com.linlay.agentplatform.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.memory.ChatWindowMemoryProperties;
import com.linlay.agentplatform.memory.ChatWindowMemoryStore;
import com.linlay.agentplatform.service.LlmService;
import com.linlay.agentplatform.tool.PlatformCreateAgent;
import com.linlay.agentplatform.tool.SystemBash;
import com.linlay.agentplatform.tool.ToolRegistry;
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
        Path agentsDir = tempDir.resolve("agents");
        Files.createDirectories(agentsDir);

        Files.writeString(agentsDir.resolve("demo_one.json"), """
                {
                  "description": "demo one",
                  "key": "demo_one",
                  "modelConfig": {
                    "modelKey": "bailian-qwen3-max"
                  },
                  "toolConfig": {
                    "backends": ["_bash_"],
                    "frontends": [],
                    "actions": []
                  },
                  "mode": "ONESHOT",
                  "plain": { "systemPrompt": "你是 demo one" }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(agentsDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);
        LlmService llmService = new LlmService(null, null);
        ToolRegistry toolRegistry = new ToolRegistry(List.of(
                new SystemBash(),
                new PlatformCreateAgent(agentsDir)
        ));
        ChatWindowMemoryProperties memoryProperties = new ChatWindowMemoryProperties();
        memoryProperties.setDir(tempDir.resolve("chats").toString());
        ChatWindowMemoryStore memoryStore = new ChatWindowMemoryStore(new ObjectMapper(), memoryProperties);

        AgentRegistry registry = new AgentRegistry(
                loader,
                llmService,
                toolRegistry,
                new ObjectMapper(),
                memoryStore,
                null,
                null,
                null
        );

        assertThat(registry.listIds()).contains("demo_one");
        assertThatThrownBy(() -> registry.get("demo_two"))
                .isInstanceOf(IllegalArgumentException.class);

        Files.writeString(agentsDir.resolve("demo_two.json"), """
                {
                  "description": "demo two",
                  "key": "demo_two",
                  "modelConfig": {
                    "modelKey": "bailian-qwen3-max"
                  },
                  "toolConfig": {
                    "backends": ["_bash_"],
                    "frontends": [],
                    "actions": []
                  },
                  "mode": "REACT",
                  "react": { "systemPrompt": "你是 demo two" }
                }
                """);

        assertThatThrownBy(() -> registry.get("demo_two"))
                .isInstanceOf(IllegalArgumentException.class);

        registry.refreshAgents();
        assertThat(registry.get("demo_two").id()).isEqualTo("demo_two");

        Files.delete(agentsDir.resolve("demo_two.json"));
        assertThat(registry.listIds()).contains("demo_two");

        registry.refreshAgents();
        assertThatThrownBy(() -> registry.get("demo_two"))
                .isInstanceOf(IllegalArgumentException.class);
    }
}
