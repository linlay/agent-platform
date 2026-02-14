package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.mode.PlanExecuteMode;
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
    void shouldLoadExternalAgentWithKeyNameIcon() throws IOException {
        Files.writeString(tempDir.resolve("ops_daily.json"), """
                {
                  "key": "ops_daily",
                  "name": "运维日报助手",
                  "icon": "emoji:📅",
                  "description": "运维助手",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAN_EXECUTE",
                  "tools": ["bash"],
                  "planExecute": {
                    "plan": { "systemPrompt": "先规划" },
                    "execute": { "systemPrompt": "再执行" },
                    "summary": { "systemPrompt": "最后总结" }
                  }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("ops_daily");
        AgentDefinition definition = byId.get("ops_daily");
        assertThat(definition.name()).isEqualTo("运维日报助手");
        assertThat(definition.icon()).isEqualTo("emoji:📅");
        assertThat(definition.mode()).isEqualTo(AgentRuntimeMode.PLAN_EXECUTE);
        assertThat(definition.tools()).containsExactly("bash");
        assertThat(definition.agentMode()).isInstanceOf(PlanExecuteMode.class);

        PlanExecuteMode peMode = (PlanExecuteMode) definition.agentMode();
        assertThat(peMode.planStage().systemPrompt()).isEqualTo("先规划");
        assertThat(peMode.executeStage().systemPrompt()).isEqualTo("再执行");
        assertThat(peMode.summaryStage().systemPrompt()).isEqualTo("最后总结");
    }

    @Test
    void shouldRejectLegacyAgentConfig() throws IOException {
        Files.writeString(tempDir.resolve("legacy.json"), """
                {
                  "description":"legacy",
                  "providerKey":"bailian",
                  "model":"qwen3-max",
                  "mode":"PLAIN",
                  "plain":{"systemPrompt":"旧模式"}
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);
        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).doesNotContainKey("legacy");
    }

    @Test
    void shouldLoadTripleQuotedPromptForOneshot() throws IOException {
        Files.writeString(tempDir.resolve("fortune_teller.json"), "{" + "\n"
                + "  \"key\": \"fortune_teller\",\n"
                + "  \"description\": \"算命大师\",\n"
                + "  \"providerKey\": \"bailian\",\n"
                + "  \"model\": \"qwen3-max\",\n"
                + "  \"mode\": \"ONESHOT\",\n"
                + "  \"plain\": {\n"
                + "    \"systemPrompt\": \"\"\"\n"
                + "你是算命大师\n"
                + "请先问出生日期\n"
                + "\"\"\"\n"
                + "  }\n"
                + "}\n");

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("fortune_teller");
        assertThat(byId.get("fortune_teller").systemPrompt()).isEqualTo("你是算命大师\n请先问出生日期");
        assertThat(byId.get("fortune_teller").mode()).isEqualTo(AgentRuntimeMode.ONESHOT);
    }

    @Test
    void shouldParseAllThreeModes() throws IOException {
        Files.writeString(tempDir.resolve("m_oneshot.json"), """
                {
                  "key": "m_oneshot",
                  "description": "oneshot",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "ONESHOT",
                  "plain": { "systemPrompt": "oneshot prompt" }
                }
                """);
        Files.writeString(tempDir.resolve("m_react.json"), """
                {
                  "key": "m_react",
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
                  "key": "m_plan_execute",
                  "description": "plan execute",
                  "providerKey": "bailian",
                  "model": "qwen3-max",
                  "mode": "PLAN_EXECUTE",
                  "planExecute": {
                    "plan": { "systemPrompt": "plan prompt" },
                    "execute": { "systemPrompt": "execute prompt" },
                    "summary": { "systemPrompt": "summary prompt" }
                  }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);
        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).hasSize(3);
        assertThat(byId.get("m_oneshot").mode()).isEqualTo(AgentRuntimeMode.ONESHOT);
        assertThat(byId.get("m_react").mode()).isEqualTo(AgentRuntimeMode.REACT);
        assertThat(byId.get("m_plan_execute").mode()).isEqualTo(AgentRuntimeMode.PLAN_EXECUTE);
    }
}
