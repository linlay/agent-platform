package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.stream.Collectors;

import static org.assertj.core.api.Assertions.assertThat;

class AgentDefinitionLoaderTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldLoadExternalV2AgentByFileName() throws IOException {
        Path file = tempDir.resolve("ops_daily.json");
        Files.writeString(file, """
                {
                  "description": "运维助手",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAN_EXECUTE",
                  "tools": ["bash"],
                  "planExecute": {
                    "planSystemPrompt": "先规划",
                    "executeSystemPrompt": "再执行",
                    "summarySystemPrompt": "最后总结"
                  }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("ops_daily");
        assertThat(byId.get("ops_daily").mode()).isEqualTo(AgentRuntimeMode.PLAN_EXECUTE);
        assertThat(byId.get("ops_daily").tools()).containsExactly("bash");
        assertThat(byId.get("ops_daily").promptSet().planSystemPrompt()).isEqualTo("先规划");
        assertThat(byId.get("ops_daily").promptSet().executeSystemPrompt()).isEqualTo("再执行");
        assertThat(byId.get("ops_daily").promptSet().summarySystemPrompt()).isEqualTo("最后总结");
    }

    @Test
    void shouldRejectLegacyAgentConfig() throws IOException {
        Path file = tempDir.resolve("legacy.json");
        Files.writeString(file, """
                {
                  "description":"legacy",
                  "providerType":"BAILIAN",
                  "model":"qwen3-max",
                  "systemPrompt":"你是旧配置",
                  "deepThink":false
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);
        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).doesNotContainKey("legacy");
    }

    @Test
    void shouldLoadTripleQuotedPromptForNestedModeField() throws IOException {
        Path file = tempDir.resolve("fortune_teller.json");
        Files.writeString(file, "{" + "\n"
                + "  \"description\": \"算命大师\",\n"
                + "  \"providerKey\": \"bailian\",\n"
                + "  \"model\": \"qwen3-max\",\n"
                + "  \"mode\": \"THINKING\",\n"
                + "  \"thinking\": {\n"
                + "    \"systemPrompt\": \"\"\"\n"
                + "你是算命大师\n"
                + "请先问出生日期\n"
                + "\"\"\",\n"
                + "    \"exposeReasoningToUser\": true\n"
                + "  }\n"
                + "}\n");

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("fortune_teller");
        assertThat(byId.get("fortune_teller").systemPrompt()).isEqualTo("你是算命大师\n请先问出生日期");
        assertThat(byId.get("fortune_teller").mode()).isEqualTo(AgentRuntimeMode.THINKING);
    }

    @Test
    void shouldParseAllSixV2Modes() throws IOException {
        Files.writeString(tempDir.resolve("m_plain.json"), """
                {
                  "description": "plain",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAIN",
                  "plain": { "systemPrompt": "plain prompt" }
                }
                """);
        Files.writeString(tempDir.resolve("m_thinking.json"), """
                {
                  "description": "thinking",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "THINKING",
                  "thinking": {
                    "systemPrompt": "thinking prompt",
                    "exposeReasoningToUser": true
                  }
                }
                """);
        Files.writeString(tempDir.resolve("m_plain_tooling.json"), """
                {
                  "description": "plain tooling",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAIN_TOOLING",
                  "tools": ["bash"],
                  "plainTooling": { "systemPrompt": "plain tooling prompt" }
                }
                """);
        Files.writeString(tempDir.resolve("m_thinking_tooling.json"), """
                {
                  "description": "thinking tooling",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "THINKING_TOOLING",
                  "tools": ["bash"],
                  "thinkingTooling": {
                    "systemPrompt": "thinking tooling prompt",
                    "exposeReasoningToUser": false
                  }
                }
                """);
        Files.writeString(tempDir.resolve("m_react.json"), """
                {
                  "description": "react",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "REACT",
                  "react": {
                    "systemPrompt": "react prompt",
                    "maxSteps": 5
                  }
                }
                """);
        Files.writeString(tempDir.resolve("m_plan_execute.json"), """
                {
                  "description": "plan execute",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAN_EXECUTE",
                  "planExecute": {
                    "planSystemPrompt": "plan prompt",
                    "executeSystemPrompt": "execute prompt",
                    "summarySystemPrompt": "summary prompt"
                  }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);
        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).hasSize(6);
        assertThat(byId.get("m_plain").mode()).isEqualTo(AgentRuntimeMode.PLAIN);
        assertThat(byId.get("m_thinking").mode()).isEqualTo(AgentRuntimeMode.THINKING);
        assertThat(byId.get("m_plain_tooling").mode()).isEqualTo(AgentRuntimeMode.PLAIN_TOOLING);
        assertThat(byId.get("m_thinking_tooling").mode()).isEqualTo(AgentRuntimeMode.THINKING_TOOLING);
        assertThat(byId.get("m_react").mode()).isEqualTo(AgentRuntimeMode.REACT);
        assertThat(byId.get("m_plan_execute").mode()).isEqualTo(AgentRuntimeMode.PLAN_EXECUTE);
        assertThat(byId.get("m_plan_execute").promptSet().planSystemPrompt()).isEqualTo("plan prompt");
        assertThat(byId.get("m_plan_execute").promptSet().executeSystemPrompt()).isEqualTo("execute prompt");
        assertThat(byId.get("m_plan_execute").promptSet().summarySystemPrompt()).isEqualTo("summary prompt");
    }
}
