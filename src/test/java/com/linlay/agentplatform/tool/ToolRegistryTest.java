package com.linlay.agentplatform.tool;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.ZonedDateTime;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.beans.factory.support.StaticListableBeanFactory;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.AgentCatalogProperties;
import com.linlay.agentplatform.config.AgentFileCreateToolProperties;
import com.linlay.agentplatform.config.AgentProviderProperties;
import com.linlay.agentplatform.config.CapabilityCatalogProperties;
import com.linlay.agentplatform.config.ViewportCatalogProperties;
import com.linlay.agentplatform.model.ModelCatalogProperties;
import com.linlay.agentplatform.model.ModelRegistryService;
import com.linlay.agentplatform.service.RuntimeResourceSyncService;

class ToolRegistryTest {

    private final ObjectMapper objectMapper = new ObjectMapper();
    private final CityDateTime cityDateTimeTool = new CityDateTime();
    private final MockCityWeatherTool cityWeatherTool = new MockCityWeatherTool();
    private final MockLogisticsStatusTool logisticsStatusTool = new MockLogisticsStatusTool();
    private final MockTransportScheduleTool transportScheduleTool = new MockTransportScheduleTool();
    private final MockTodoTasksTool todoTasksTool = new MockTodoTasksTool();
    private final MockSensitiveDataDetectorTool sensitiveDataDetectorTool = new MockSensitiveDataDetectorTool();
    private final SystemBash bashTool = new SystemBash();

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
    void sameArgsShouldReturnSameTransportScheduleJson() {
        Map<String, Object> args = Map.of("type", "flight", "fromCity", "Shanghai", "toCity", "Beijing", "date", "2026-02-14");

        JsonNode first = transportScheduleTool.invoke(args);
        JsonNode second = transportScheduleTool.invoke(args);

        assertThat(first).isEqualTo(second);
    }

    @Test
    void logisticsToolShouldReturnStructuredPayload() {
        JsonNode result = logisticsStatusTool.invoke(Map.of("trackingNo", "YT1234567890"));

        assertThat(result.path("trackingNo").asText()).isEqualTo("YT1234567890");
        assertThat(result.path("carrier").asText()).isNotBlank();
        assertThat(result.path("status").asText()).isNotBlank();
        assertThat(result.path("currentNode").asText()).isNotBlank();
        assertThat(result.path("etaDate").asText()).isNotBlank();
        assertThat(result.path("updatedAt").asText()).isNotBlank();
        assertThat(result.path("mockTag").asText()).isEqualTo("idempotent-random-json");
    }

    @Test
    void transportToolShouldReturnStructuredPayload() {
        JsonNode result = transportScheduleTool.invoke(Map.of(
                "type", "train",
                "fromCity", "Shanghai",
                "toCity", "Nanjing",
                "date", "2026-02-15"
        ));

        assertThat(result.path("travelType").asText()).isEqualTo("高铁");
        assertThat(result.path("fromCity").asText()).isEqualTo("上海");
        assertThat(result.path("toCity").asText()).isEqualTo("南京");
        assertThat(result.path("number").asText()).isNotBlank();
        assertThat(result.path("departureTime").asText()).matches("\\d{2}:\\d{2}");
        assertThat(result.path("arrivalTime").asText()).matches("\\d{2}:\\d{2}");
        assertThat(result.path("status").asText()).isNotBlank();
        assertThat(result.path("gateOrPlatform").asText()).isNotBlank();
    }

    @Test
    void todoTasksToolShouldReturnTaskArray() {
        JsonNode result = todoTasksTool.invoke(Map.of("owner", "张三"));

        assertThat(result.path("owner").asText()).isEqualTo("张三");
        assertThat(result.path("total").asInt()).isGreaterThanOrEqualTo(3);
        assertThat(result.path("tasks").isArray()).isTrue();
        assertThat(result.path("tasks")).isNotEmpty();
        assertThat(result.path("tasks").get(0).path("title").asText()).isNotBlank();
        assertThat(result.path("tasks").get(0).path("priority").asText()).isNotBlank();
        assertThat(result.path("tasks").get(0).path("status").asText()).isNotBlank();
        assertThat(result.path("tasks").get(0).path("dueDate").asText()).isNotBlank();
    }

    @Test
    void cityDateTimeShouldReturnRealtimeTimeWithTimezone() {
        Map<String, Object> args = Map.of("city", "Shanghai");

        JsonNode result = cityDateTimeTool.invoke(args);

        assertThat(result.path("timezone").asText()).isEqualTo("UTC+8");
        assertThat(result.path("source").asText()).isEqualTo("system-clock");
        assertThat(result.path("date").asText()).isNotBlank();
        assertThat(result.path("weekday").asText()).startsWith("星期");
        assertThat(result.path("lunarDate").asText()).isNotBlank();
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
                      "afterCallHint": "use city datetime prompt",
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
        CapabilityRegistryService capabilityRegistryService = new CapabilityRegistryService(
                new ObjectMapper(),
                properties,
                createRuntimeResourceSyncService(tempDir, toolsDir)
        );

        StaticListableBeanFactory beanFactory = new StaticListableBeanFactory();
        beanFactory.addBean("capabilityRegistryService", capabilityRegistryService);
        ToolRegistry toolRegistry = new ToolRegistry(
                List.of(new CityDateTime()),
                beanFactory.getBeanProvider(CapabilityRegistryService.class)
        );

        BaseTool cityTool = toolRegistry.list().stream()
                .filter(tool -> "city_datetime".equals(tool.name()))
                .findFirst()
                .orElseThrow();

        assertThat(cityTool.description()).isEqualTo("city datetime from backend");
        assertThat(cityTool.afterCallHint()).isEqualTo("use city datetime prompt");
        assertThat(cityTool.parametersSchema().get("required")).isEqualTo(List.of("city"));
        assertThat(toolRegistry.invoke("city_datetime", Map.of("city", "Shanghai")).path("timezone").asText())
                .isEqualTo("UTC+8");
    }

    @Test
    void bashToolShouldRejectWhenAllowedCommandsAreNotConfigured() {
        JsonNode result = bashTool.invoke(Map.of("command", "cat /etc/passwd"));
        assertThat(result.isTextual()).isTrue();
        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("agent.tools.bash.allowed-commands");
    }

    @Test
    void bashToolShouldRejectDefaultLsWhenWhitelistIsEmpty() {
        JsonNode result = bashTool.invoke(Map.of("command", "ls"));
        assertThat(result.isTextual()).isTrue();
        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("agent.tools.bash.allowed-commands");
    }

    @Test
    void bashToolShouldRejectRelativePathWhenWorkingDirectoryNotInAllowedPaths(@TempDir Path tempDir) throws IOException {
        Path file = tempDir.resolve("demo.txt");
        Files.writeString(file, "hello");
        SystemBash localBashTool = new SystemBash(tempDir, List.of(), Set.of("cat"), Set.of());

        JsonNode result = localBashTool.invoke(Map.of("command", "cat demo.txt"));

        assertThat(result.isTextual()).isTrue();
        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("Path not allowed outside authorized directories");
    }

    @Test
    void bashToolShouldAllowRelativeAndAbsolutePathsInsideAllowedPaths(@TempDir Path tempDir) throws IOException {
        Path file = tempDir.resolve("demo.txt");
        Files.writeString(file, "hello");
        SystemBash localBashTool = new SystemBash(tempDir, List.of(tempDir), Set.of("cat"), Set.of());

        JsonNode relativeResult = localBashTool.invoke(Map.of("command", "cat demo.txt"));
        JsonNode absoluteResult = localBashTool.invoke(Map.of("command", "cat " + file));

        assertThat(relativeResult.asText()).contains("exitCode: 0");
        assertThat(relativeResult.asText()).contains("hello");
        assertThat(absoluteResult.asText()).contains("exitCode: 0");
        assertThat(absoluteResult.asText()).contains("hello");
    }

    @Test
    void bashToolShouldAllowConfiguredAbsolutePath(@TempDir Path tempDir) throws IOException {
        Path externalDir = tempDir.resolve("opt");
        Files.createDirectories(externalDir);
        Path keyFile = externalDir.resolve("demo.key");
        Files.writeString(keyFile, "secret");

        SystemBash localBashTool = new SystemBash(
                Path.of(System.getProperty("user.dir", ".")),
                List.of(externalDir),
                Set.of("cat"),
                Set.of()
        );
        JsonNode result = localBashTool.invoke(Map.of("command", "cat " + keyFile));

        assertThat(result.isTextual()).isTrue();
        assertThat(result.asText()).contains("exitCode: 0");
        assertThat(result.asText()).contains("secret");
    }

    @Test
    void bashToolShouldUseAllowedCommandsAsDefaultPathCheckedCommands(@TempDir Path tempDir) throws IOException {
        Path file = tempDir.resolve("demo.txt");
        Files.writeString(file, "hello");
        SystemBash localBashTool = new SystemBash(tempDir, List.of(), Set.of("cat"), Set.of());

        JsonNode result = localBashTool.invoke(Map.of("command", "cat " + file));

        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("Path not allowed outside authorized directories");
    }

    @Test
    void bashToolShouldAllowCustomConfiguredCommands(@TempDir Path tempDir) {
        SystemBash localBashTool = new SystemBash(tempDir, List.of(), Set.of("echo", "ls"), Set.of("ls"));

        JsonNode echoResult = localBashTool.invoke(Map.of("command", "echo hello"));
        assertThat(echoResult.asText()).contains("exitCode: 0");
        assertThat(echoResult.asText()).contains("hello");

        JsonNode catResult = localBashTool.invoke(Map.of("command", "cat somefile"));
        assertThat(catResult.asText()).contains("Command not allowed: cat");
    }

    @Test
    void agentFileCreateToolShouldWriteAgentJson(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        PlatformCreateAgent tool = new PlatformCreateAgent(agentsDir);

        JsonNode result = tool.invoke(Map.of(
                "agentId", "qa_bot",
                "description", "QA 助手",
                "modelKey", "openai-gpt35",
                "systemPrompt", "你是 QA 助手\n请先问清问题",
                "mode", "ONESHOT"
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("created").asBoolean()).isTrue();
        assertThat(result.path("agentId").asText()).isEqualTo("qa_bot");

        Path file = agentsDir.resolve("qa_bot.json");
        assertThat(Files.exists(file)).isTrue();
        JsonNode content = objectMapper.readTree(Files.readString(file));
        assertThat(content.path("description").asText()).isEqualTo("QA 助手");
        assertThat(content.path("modelConfig").path("modelKey").asText()).isEqualTo("openai-gpt35");
        assertThat(content.path("mode").asText()).isEqualTo("ONESHOT");
        assertThat(content.path("plain").path("systemPrompt").asText()).isEqualTo("你是 QA 助手\n请先问清问题");
        assertThat(content.has("toolConfig")).isFalse();
    }

    @Test
    void agentFileCreateToolShouldRejectInvalidAgentId(@TempDir Path tempDir) {
        PlatformCreateAgent tool = new PlatformCreateAgent(tempDir.resolve("agents"));

        JsonNode result = tool.invoke(Map.of("agentId", "../escape"));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("Invalid agentId");
    }

    @Test
    void agentFileCreateToolShouldDefaultModelKeyFromFirstProvider(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        Path modelsDir = tempDir.resolve("models");
        Path toolsDir = tempDir.resolve("tools");
        Files.createDirectories(modelsDir);
        Files.createDirectories(toolsDir);
        Files.writeString(modelsDir.resolve("zhipu-glm-4-plus.json"), """
                {
                  "key": "zhipu-glm-4-plus",
                  "provider": "zhipu",
                  "protocol": "OPENAI",
                  "modelId": "glm-4-plus"
                }
                """);
        Files.writeString(modelsDir.resolve("bailian-qwen3-max.json"), """
                {
                  "key": "bailian-qwen3-max",
                  "provider": "bailian",
                  "protocol": "OPENAI",
                  "modelId": "qwen3-max"
                }
                """);

        AgentProviderProperties providerProperties = new AgentProviderProperties();
        LinkedHashMap<String, AgentProviderProperties.ProviderConfig> providers = new LinkedHashMap<>();
        AgentProviderProperties.ProviderConfig zhipu = new AgentProviderProperties.ProviderConfig();
        zhipu.setBaseUrl("https://example.com/v1");
        zhipu.setApiKey("test-zhipu-key");
        zhipu.setModel("glm-4-plus");
        providers.put("zhipu", zhipu);
        AgentProviderProperties.ProviderConfig bailian = new AgentProviderProperties.ProviderConfig();
        bailian.setBaseUrl("https://example.com/v1");
        bailian.setApiKey("test-bailian-key");
        bailian.setModel("qwen3-max");
        providers.put("bailian", bailian);
        providerProperties.setProviders(providers);

        ModelCatalogProperties modelProperties = new ModelCatalogProperties();
        modelProperties.setExternalDir(modelsDir.toString());
        ModelRegistryService modelRegistryService = new ModelRegistryService(
                objectMapper,
                modelProperties,
                providerProperties,
                createRuntimeResourceSyncService(tempDir, toolsDir)
        );

        AgentCatalogProperties agentProperties = new AgentCatalogProperties();
        agentProperties.setExternalDir(agentsDir.toString());
        AgentFileCreateToolProperties toolProperties = new AgentFileCreateToolProperties();
        PlatformCreateAgent tool = new PlatformCreateAgent(
                agentProperties,
                toolProperties,
                providerProperties,
                modelRegistryService
        );

        JsonNode result = tool.invoke(Map.of(
                "agentId", "fortune_bot",
                "description", "算命大师",
                "systemPrompt", "你是算命大师",
                "mode", "ONESHOT"
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        JsonNode content = objectMapper.readTree(Files.readString(agentsDir.resolve("fortune_bot.json")));
        assertThat(content.path("modelConfig").path("modelKey").asText()).isEqualTo("zhipu-glm-4-plus");
        assertThat(content.path("mode").asText()).isEqualTo("ONESHOT");
    }

    @Test
    void agentFileCreateToolShouldUseConfiguredDefaultSystemPrompt(@TempDir Path tempDir) throws IOException {
        Path agentsDir = tempDir.resolve("agents");
        PlatformCreateAgent tool = new PlatformCreateAgent(agentsDir, "这是配置的默认提示词");

        JsonNode result = tool.invoke(Map.of(
                "agentId", "default_prompt_bot",
                "modelKey", "bailian-qwen3-max",
                "mode", "ONESHOT"
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        JsonNode content = objectMapper.readTree(Files.readString(agentsDir.resolve("default_prompt_bot.json")));
        assertThat(content.path("plain").path("systemPrompt").asText()).isEqualTo("这是配置的默认提示词");
    }

    @Test
    void sensitiveDataDetectorShouldReturnPositiveForLongSensitiveText() {
        String longText = "这是一大段业务文本，用于模拟超长入参。".repeat(120)
                + "联系人邮箱是 test.user@example.com ，请尽快处理。";

        JsonNode result = sensitiveDataDetectorTool.invoke(Map.of("text", longText));

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

    private RuntimeResourceSyncService createRuntimeResourceSyncService(Path root, Path toolsDir) {
        AgentCatalogProperties agentProperties = new AgentCatalogProperties();
        agentProperties.setExternalDir(root.resolve("agents").toString());
        ViewportCatalogProperties viewportProperties = new ViewportCatalogProperties();
        viewportProperties.setExternalDir(root.resolve("viewports").toString());
        CapabilityCatalogProperties capabilityProperties = new CapabilityCatalogProperties();
        capabilityProperties.setToolsExternalDir(toolsDir.toString());
        ModelCatalogProperties modelProperties = new ModelCatalogProperties();
        modelProperties.setExternalDir(root.resolve("models").toString());
        return new RuntimeResourceSyncService(agentProperties, viewportProperties, capabilityProperties, modelProperties);
    }
}
