package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
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
    void shouldLoadExternalAgentByFileName() throws IOException {
        Path file = tempDir.resolve("ops_daily.json");
        Files.writeString(file, """
                {
                  "providerType":"BAILIAN",
                  "model":"qwen3-max",
                  "systemPrompt":"你是运维助手",
                  "mode":"PLAN_EXECUTE",
                  "tools":["bash","mock_ops_runbook"]
                }
                """);

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("ops_daily");
        assertThat(byId.get("ops_daily").mode()).isEqualTo(AgentMode.PLAN_EXECUTE);
        assertThat(byId.get("ops_daily").tools()).containsExactly("bash", "mock_ops_runbook");
        assertThat(byId.get("ops_daily").systemPrompt()).isEqualTo("你是运维助手");
    }

    @Test
    void shouldIncludeBuiltInAgents() {
        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("agentCreator");
        assertThat(byId.get("agentCreator").mode()).isEqualTo(AgentMode.PLAN_EXECUTE);
        assertThat(byId.get("agentCreator").tools()).containsExactly("agent_file_create");

        assertThat(byId).containsKey("demoViewport");
        assertThat(byId.get("demoViewport").mode()).isEqualTo(AgentMode.PLAN_EXECUTE);
        assertThat(byId.get("demoViewport").tools())
                .containsExactly("city_datetime", "mock_city_weather", "mock_logistics_status");
        assertThat(byId.get("demoViewport").systemPrompt()).contains("```viewport");
        assertThat(byId.get("demoViewport").systemPrompt()).contains("type 只能是 html/qlc/dqlc");
        assertThat(byId.get("demoViewport").systemPrompt()).contains("description 中声明的 key=");
        assertThat(byId.get("demoViewport").systemPrompt()).doesNotContain("show_weather_card");

        assertThat(byId).containsKey("demoAction");
        assertThat(byId.get("demoAction").mode()).isEqualTo(AgentMode.PLAIN);
        assertThat(byId.get("demoAction").tools()).containsExactly("switch_theme", "launch_fireworks", "show_modal");
        assertThat(byId.get("demoAction").systemPrompt()).contains("launch_fireworks");
        assertThat(byId.get("demoAction").systemPrompt()).contains("show_modal");

        assertThat(byId).containsKey("demoPlain");
        assertThat(byId.get("demoPlain").tools()).doesNotContain("show_weather_card");
        assertThat(byId).containsKey("demoReAct");
        assertThat(byId.get("demoReAct").tools()).doesNotContain("show_weather_card");
    }

    @Test
    void shouldLoadExternalAgentWithTripleQuotedSystemPrompt() throws IOException {
        Path file = tempDir.resolve("fortune_teller.json");
        Files.writeString(file, "{\n"
                + "  \"description\": \"算命大师\",\n"
                + "  \"providerType\": \"BAILIAN\",\n"
                + "  \"model\": \"qwen3-max\",\n"
                + "  \"systemPrompt\": \"\"\"\n"
                + "你是算命大师\n"
                + "请先问出生日期\n"
                + "\"\"\",\n"
                + "  \"deepThink\": false\n"
                + "}\n");

        AgentCatalogProperties properties = new AgentCatalogProperties();
        properties.setExternalDir(tempDir.toString());

        AgentDefinitionLoader loader = new AgentDefinitionLoader(new ObjectMapper(), properties);

        Map<String, AgentDefinition> byId = loader.loadAll().stream()
                .collect(Collectors.toMap(AgentDefinition::id, definition -> definition));

        assertThat(byId).containsKey("fortune_teller");
        assertThat(byId.get("fortune_teller").systemPrompt()).isEqualTo("你是算命大师\n请先问出生日期");
        assertThat(byId.get("fortune_teller").mode()).isEqualTo(AgentMode.PLAIN);
    }
}
