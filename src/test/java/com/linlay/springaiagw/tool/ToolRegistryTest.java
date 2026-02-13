package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.JsonNode;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.beans.factory.support.StaticListableBeanFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.ZonedDateTime;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;

class ToolRegistryTest {

    private final CityDateTimeTool cityDateTimeTool = new CityDateTimeTool();
    private final MockCityWeatherTool cityWeatherTool = new MockCityWeatherTool();
    private final MockLogisticsStatusTool logisticsStatusTool = new MockLogisticsStatusTool();
    private final MockSensitiveDataDetectorTool sensitiveDataDetectorTool = new MockSensitiveDataDetectorTool();
    private final BashTool bashTool = new BashTool();

    @Test
    void sameArgsShouldReturnSameWeatherJson() {
        Map<String, Object> args = Map.of("city", "Shanghai", "date", "2026-02-09");

        JsonNode first = cityWeatherTool.invoke(args);
        JsonNode second = cityWeatherTool.invoke(args);

        assertThat(first).isEqualTo(second);
    }

    @Test
    void sameArgsShouldReturnSameLogisticsJson() {
        Map<String, Object> args = Map.of("trackingNo", "SF123456789CN", "carrier", "SF Express");

        JsonNode first = logisticsStatusTool.invoke(args);
        JsonNode second = logisticsStatusTool.invoke(args);

        assertThat(first).isEqualTo(second);
    }

    @Test
    void logisticsToolShouldReturnStructuredPayload() {
        JsonNode result = logisticsStatusTool.invoke(Map.of("trackingNo", "YT1234567890"));

        assertThat(result.path("tool").asText()).isEqualTo("mock_logistics_status");
        assertThat(result.path("trackingNo").asText()).isEqualTo("YT1234567890");
        assertThat(result.path("carrier").asText()).isNotBlank();
        assertThat(result.path("status").asText()).isNotBlank();
        assertThat(result.path("currentNode").asText()).isNotBlank();
        assertThat(result.path("etaDate").asText()).isNotBlank();
        assertThat(result.path("updatedAt").asText()).isNotBlank();
        assertThat(result.path("mockTag").asText()).isEqualTo("idempotent-random-json");
    }

    @Test
    void cityDateTimeShouldReturnRealtimeTimeWithTimezone() {
        Map<String, Object> args = Map.of("city", "Shanghai");

        JsonNode result = cityDateTimeTool.invoke(args);

        assertThat(result.path("tool").asText()).isEqualTo("city_datetime");
        assertThat(result.path("timezone").asText()).isEqualTo("Asia/Shanghai");
        assertThat(result.path("source").asText()).isEqualTo("system-clock");
        assertThat(result.path("date").asText()).isNotBlank();
        assertThat(result.path("time").asText()).isNotBlank();
        assertThatCode(() -> ZonedDateTime.parse(result.path("iso").asText())).doesNotThrowAnyException();
    }

    @Test
    void backendCapabilityMetadataShouldOverrideNativeToolDefinition(@TempDir Path tempDir) throws IOException {
        Path toolsDir = tempDir.resolve("tools");
        Files.createDirectories(toolsDir);
        Files.writeString(toolsDir.resolve("city_datetime.backend"), """
                {
                  "tools": [
                    {
                      "type": "function",
                      "name": "city_datetime",
                      "description": "city datetime from backend",
                      "prompt": "use city datetime prompt",
                      "parameters": {
                        "type": "object",
                        "properties": {
                          "city": {"type": "string"}
                        },
                        "required": ["city"],
                        "additionalProperties": false
                      }
                    }
                  ]
                }
                """);

        CapabilityCatalogProperties properties = new CapabilityCatalogProperties();
        properties.setToolsExternalDir(toolsDir.toString());
        CapabilityRegistryService capabilityRegistryService = new CapabilityRegistryService(new ObjectMapper(), properties);

        StaticListableBeanFactory beanFactory = new StaticListableBeanFactory();
        beanFactory.addBean("capabilityRegistryService", capabilityRegistryService);
        ToolRegistry toolRegistry = new ToolRegistry(
                List.of(new CityDateTimeTool()),
                beanFactory.getBeanProvider(CapabilityRegistryService.class)
        );

        BaseTool cityTool = toolRegistry.list().stream()
                .filter(tool -> "city_datetime".equals(tool.name()))
                .findFirst()
                .orElseThrow();

        assertThat(cityTool.description()).isEqualTo("city datetime from backend");
        assertThat(cityTool.prompt()).isEqualTo("use city datetime prompt");
        assertThat(cityTool.parametersSchema().get("required")).isEqualTo(List.of("city"));
        assertThat(toolRegistry.invoke("city_datetime", Map.of("city", "Shanghai")).path("tool").asText())
                .isEqualTo("city_datetime");
    }

    @Test
    void bashToolShouldRejectUnlistedCommand() {
        JsonNode result = bashTool.invoke(Map.of("command", "cat /etc/passwd"));
        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("outside authorized directories");
    }

    @Test
    void bashToolShouldRunAllowedLsCommand() {
        JsonNode result = bashTool.invoke(Map.of("command", "ls"));
        assertThat(result.path("tool").asText()).isEqualTo("bash");
        assertThat(result.path("exitCode").asInt()).isEqualTo(0);
    }

    @Test
    void bashToolShouldReadLocalFile(@TempDir Path tempDir) throws IOException {
        Path file = tempDir.resolve("demo.txt");
        Files.writeString(file, "hello");
        BashTool localBashTool = new BashTool(tempDir);

        JsonNode result = localBashTool.invoke(Map.of("command", "cat demo.txt"));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("stdout").asText()).contains("hello");
    }

    @Test
    void bashToolShouldExpandGlobForCat(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        Files.createDirectories(agentsDir);
        Files.writeString(agentsDir.resolve("a.json"), "{\"name\":\"a\"}\n");
        Files.writeString(agentsDir.resolve("b.json"), "{\"name\":\"b\"}\n");
        BashTool localBashTool = new BashTool(tempDir);

        JsonNode result = localBashTool.invoke(Map.of("command", "cat agents/*"));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("stdout").asText()).contains("\"name\":\"a\"");
        assertThat(result.path("stdout").asText()).contains("\"name\":\"b\"");
    }

    @Test
    void bashToolShouldAllowConfiguredAbsolutePath(@TempDir Path tempDir) throws IOException {
        Path externalDir = tempDir.resolve("opt");
        Files.createDirectories(externalDir);
        Path keyFile = externalDir.resolve("demo.key");
        Files.writeString(keyFile, "secret");

        BashTool localBashTool = new BashTool(Path.of(System.getProperty("user.dir", ".")), List.of(externalDir));
        JsonNode result = localBashTool.invoke(Map.of("command", "cat " + keyFile));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("stdout").asText()).contains("secret");
    }

    @Test
    void agentFileCreateToolShouldWriteAgentJson(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        AgentFileCreateTool tool = new AgentFileCreateTool(agentsDir);

        JsonNode result = tool.invoke(Map.of(
                "agentId", "qa_bot",
                "description", "QA 助手",
                "providerType", "openai",
                "model", "gpt-3.5-turbo",
                "systemPrompt", "你是 QA 助手\n请先问清问题",
                "mode", "chat"
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("created").asBoolean()).isTrue();
        assertThat(result.path("agentId").asText()).isEqualTo("qa_bot");

        Path file = agentsDir.resolve("qa_bot.json");
        assertThat(Files.exists(file)).isTrue();
        String content = Files.readString(file);
        assertThat(content).contains("\"description\": \"QA 助手\"");
        assertThat(content).contains("\"providerType\": \"OPENAI\"");
        assertThat(content).contains("\"model\": \"gpt-3.5-turbo\"");
        assertThat(content).contains("\"systemPrompt\": \"\"\"");
        assertThat(content).contains("\"deepThink\": false");
        assertThat(content).doesNotContain("\"mode\":");
        assertThat(content).doesNotContain("\"tools\":");
    }

    @Test
    void agentFileCreateToolShouldRejectInvalidAgentId(@TempDir Path tempDir) {
        AgentFileCreateTool tool = new AgentFileCreateTool(tempDir.resolve("agents"));

        JsonNode result = tool.invoke(Map.of("agentId", "../escape"));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("Invalid agentId");
    }

    @Test
    void agentFileCreateToolShouldDefaultProviderToBailian(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        AgentFileCreateTool tool = new AgentFileCreateTool(agentsDir);

        JsonNode result = tool.invoke(Map.of(
                "agentId", "fortune_bot",
                "description", "算命大师",
                "systemPrompt", "你是算命大师"
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        String content = Files.readString(agentsDir.resolve("fortune_bot.json"));
        assertThat(content).contains("\"providerType\": \"BAILIAN\"");
        assertThat(content).contains("\"deepThink\": false");
    }

    @Test
    void sensitiveDataDetectorShouldReturnPositiveForLongSensitiveText() {
        String longText = "这是一大段业务文本，用于模拟超长入参。".repeat(120)
                + "联系人邮箱是 test.user@example.com ，请尽快处理。";

        JsonNode result = sensitiveDataDetectorTool.invoke(Map.of("text", longText));

        assertThat(result.path("tool").asText()).isEqualTo("mock_sensitive_data_detector");
        assertThat(result.path("hasSensitiveData").asBoolean()).isTrue();
        assertThat(result.path("result").asText()).isEqualTo("有敏感数据");
        assertThat(result.path("description").asText()).contains("检测到疑似");
    }

    @Test
    void sensitiveDataDetectorShouldReturnNegativeForSafeText() {
        JsonNode result = sensitiveDataDetectorTool.invoke(Map.of("text", "今天上海天气晴朗，适合散步。"));

        assertThat(result.path("hasSensitiveData").asBoolean()).isFalse();
        assertThat(result.path("result").asText()).isEqualTo("没有敏感数据");
    }
}
