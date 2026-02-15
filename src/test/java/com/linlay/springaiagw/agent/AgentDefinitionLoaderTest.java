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
                  "modelConfig": {
                    "providerKey": "bailian",
                    "model": "qwen3-max"
                  },
                  "toolConfig": {
                    "backends": ["_bash_"],
                    "frontends": [],
                    "actions": []
                  },
                  "mode": "PLAN_EXECUTE",
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
        assertThat(definition.tools()).containsExactly("_bash_");
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
                  "mode":"ONESHOT",
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
                + "  \"modelConfig\": {\n"
                + "    \"providerKey\": \"bailian\",\n"
                + "    \"model\": \"qwen3-max\"\n"
                + "  },\n"
                + "  \"toolConfig\": null,\n"
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
                  "modelConfig": {
                    "providerKey": "bailian",
                    "model": "qwen3-max"
                  },
                  "toolConfig": null,
                  "mode": "ONESHOT",
                  "plain": { "systemPrompt": "oneshot prompt" }
                }
                """);
        Files.writeString(tempDir.resolve("m_react.json"), """
                {
                  "key": "m_react",
                  "description": "react",
                  "modelConfig": {
                    "providerKey": "bailian",
                    "model": "qwen3-max"
                  },
                  "toolConfig": null,
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
                  "modelConfig": {
                    "providerKey": "bailian",
                    "model": "qwen3-max"
                  },
                  "toolConfig": null,
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

    @Test
    void shouldInheritStageModelAndRespectToolConfigNullDisable() throws IOException {
        Files.writeString(tempDir.resolve("inherit_plan.json"), """
                {
                  "key": "inherit_plan",
                  "description": "inherit test",
                  "modelConfig": {
                    "providerKey": "bailian",
                    "model": "qwen3-max",
                    "reasoning": { "enabled": true, "effort": "HIGH" }
                  },
                  "toolConfig": {
                    "backends": ["_bash_", "city_datetime"],
                    "frontends": [],
                    "actions": []
                  },
                  "mode": "PLAN_EXECUTE",
                  "planExecute": {
                    "plan": { "systemPrompt": "plan stage" },
                    "execute": { "systemPrompt": "execute stage", "toolConfig": null },
                    "summary": { "systemPrompt": "summary stage" }
                  }
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties, null);

        AgentDefinition definition = loader.loadAll().stream()
                .filter(item -> "inherit_plan".equals(item.id()))
                .findFirst()
                .orElseThrow();
        PlanExecuteMode mode = (PlanExecuteMode) definition.agentMode();

        assertThat(mode.planStage().providerKey()).isEqualTo("bailian");
        assertThat(mode.planStage().model()).isEqualTo("qwen3-max");
        assertThat(mode.planStage().tools()).containsExactlyInAnyOrder("_bash_", "city_datetime");

        assertThat(mode.executeStage().providerKey()).isEqualTo("bailian");
        assertThat(mode.executeStage().model()).isEqualTo("qwen3-max");
        assertThat(mode.executeStage().tools()).isEmpty();

        assertThat(mode.summaryStage().providerKey()).isEqualTo("bailian");
        assertThat(mode.summaryStage().model()).isEqualTo("qwen3-max");
        assertThat(mode.summaryStage().tools()).containsExactlyInAnyOrder("_bash_", "city_datetime");
    }
}
