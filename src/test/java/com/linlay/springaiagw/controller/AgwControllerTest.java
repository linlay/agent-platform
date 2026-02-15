package com.linlay.springaiagw.controller;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryProperties;
import com.linlay.springaiagw.config.ViewportCatalogProperties;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.service.ViewportRegistryService;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.reactive.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.context.annotation.Primary;
import org.springframework.http.MediaType;
import org.springframework.test.web.reactive.server.FluxExchangeResult;
import org.springframework.test.web.reactive.server.WebTestClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.charset.StandardCharsets;
import java.nio.file.StandardOpenOption;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import static org.assertj.core.api.Assertions.assertThat;

@SpringBootTest(
        webEnvironment = SpringBootTest.WebEnvironment.MOCK,
        properties = {
                "agent.providers.bailian.base-url=https://example.com/v1",
                "agent.providers.bailian.api-key=test-bailian-key",
                "agent.providers.bailian.model=test-bailian-model",
                "agent.providers.siliconflow.base-url=https://example.com/v1",
                "agent.providers.siliconflow.api-key=test-siliconflow-key",
                "agent.providers.siliconflow.model=test-siliconflow-model",
                "memory.chat.dir=${java.io.tmpdir}/springai-agw-test-chats-${random.uuid}",
                "agent.viewport.external-dir=${java.io.tmpdir}/springai-agw-test-viewports-${random.uuid}",
                "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agw-test-tools-${random.uuid}"
        }
)
@AutoConfigureWebTestClient
@Import(AgwControllerTest.TestLlmServiceConfig.class)
class AgwControllerTest {

    @Autowired
    private WebTestClient webTestClient;
    @Autowired
    private FrontendSubmitCoordinator frontendSubmitCoordinator;
    @Autowired
    private ViewportCatalogProperties viewportCatalogProperties;
    @Autowired
    private ViewportRegistryService viewportRegistryService;
    @Autowired
    private ChatWindowMemoryProperties chatWindowMemoryProperties;

    private final ObjectMapper objectMapper = new ObjectMapper();

    @TestConfiguration
    static class TestLlmServiceConfig {
        @Bean
        @Primary
        LlmService llmService() {
            return new LlmService(null, null) {
                @Override
                public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                    return Flux.just("这是", "测试", "输出");
                }

                @Override
                public Flux<String> streamContent(
                        ProviderType providerType,
                        String model,
                        String systemPrompt,
                        String userPrompt,
                        String stage
                ) {
                    return streamContent(providerType, model, systemPrompt, userPrompt);
                }

                @Override
                public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                    return Mono.just("{\"thinking\":\"先分析后回答\",\"plan\":[\"步骤1\"],\"toolCalls\":[]}");
                }

                @Override
                public Mono<String> completeText(
                        ProviderType providerType,
                        String model,
                        String systemPrompt,
                        String userPrompt,
                        String stage
                ) {
                    if (stage != null && stage.startsWith("agent-react-step-")) {
                        return Mono.just("{\"thinking\":\"信息已足够，直接生成最终回答\",\"action\":null,\"done\":true}");
                    }
                    return completeText(providerType, model, systemPrompt, userPrompt);
                }
            };
        }
    }

    @Test
    void agentsShouldReturnAvailableAgents() {
        webTestClient.get()
                .uri("/api/agents")
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data[0].key").exists()
                .jsonPath("$.data[0].meta.providerType").doesNotExist()
                .jsonPath("$.data[?(@.key=='demoModePlain')]").exists()
                .jsonPath("$.data[?(@.key=='demoModeThinking')]").exists()
                .jsonPath("$.data[?(@.key=='demoModePlainTooling')]").exists()
                .jsonPath("$.data[?(@.key=='demoModeThinkingTooling')]").exists()
                .jsonPath("$.data[?(@.key=='demoModeReact')]").exists()
                .jsonPath("$.data[?(@.key=='demoModePlanExecute')]").exists()
                .jsonPath("$.data[?(@.key=='demoViewport')]").exists()
                .jsonPath("$.data[?(@.key=='demoAction')]").exists()
                .jsonPath("$.data[?(@.key=='demoAgentCreator')]").exists();
    }

    @Test
    void agentShouldReturnDetailByAgentKey() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path("/api/agent").queryParam("agentKey", "demoModePlanExecute").build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.key").isEqualTo("demoModePlanExecute")
                .jsonPath("$.data.meta.providerType").isEqualTo("BAILIAN")
                .jsonPath("$.data.instructions").isEqualTo("你是高级执行助手。基于步骤上下文执行工具并产出步骤结果。");
    }

    @Test
    void queryShouldStreamByDefaultWithoutAcceptOrStreamParam() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlanExecute",
                        "message", "查询上海天气"
                ))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().contentTypeCompatibleWith(MediaType.TEXT_EVENT_STREAM)
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(joined).contains("\"type\":\"request.query\"");
        assertThat(joined).contains("\"type\":\"run.start\"");
        assertThat(joined).contains("\"type\":\"run.complete\"");

        String requestId = extractFirstValue(joined, "requestId");
        String runId = extractFirstValue(joined, "runId");
        assertThat(requestId).isNotBlank();
        assertThat(runId).isNotBlank();
        assertThat(requestId).isEqualTo(runId);
    }

    @Test
    void queryShouldEmitContentDeltaPerUpstreamChunkForPlainAgent() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlain",
                        "message", "流式测试"
                ))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().contentTypeCompatibleWith(MediaType.TEXT_EVENT_STREAM)
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(800)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(countOccurrences(joined, "\"type\":\"content.delta\"")).isEqualTo(3);
        int lastContentIndex = joined.lastIndexOf("\"type\":\"content.delta\"");
        int runCompleteIndex = joined.lastIndexOf("\"type\":\"run.complete\"");
        assertThat(lastContentIndex).isGreaterThanOrEqualTo(0);
        assertThat(runCompleteIndex).isGreaterThan(lastContentIndex);
    }

    @Test
    void queryShouldUseUuidChatIdWhenProvided() {
        String chatId = "123e4567-e89b-12d3-a456-426614174000";
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "requestId", "req_001",
                        "chatId", chatId,
                        "agentKey", "demoModePlanExecute",
                        "message", "查询上海天气"
                ))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(joined).contains("\"type\":\"request.query\"");
        assertThat(joined).contains("\"type\":\"run.start\"");
        assertThat(joined).contains("\"type\":\"run.complete\"");
        assertThat(joined).contains("\"chatId\":\"" + chatId + "\"");
    }

    @Test
    void queryShouldRejectInvalidChatId() {
        webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlanExecute",
                        "chatId", "not-a-uuid",
                        "message", "查询上海天气"
                ))
                .exchange()
                .expectStatus().isBadRequest();
                // query 接口为 SSE，错误时只校验状态码
    }

    @Test
    void submitShouldReturnAcceptedAck() {
        String runId = "123e4567-e89b-12d3-a456-426614174001";
        String toolId = "tool_abc";
        Mono<Object> pending = frontendSubmitCoordinator.awaitSubmit(runId, toolId).cache();
        pending.subscribe();

        webTestClient.post()
                .uri("/api/submit")
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "requestId", "req_submit_001",
                        "chatId", "123e4567-e89b-12d3-a456-426614174000",
                        "runId", runId,
                        "toolId", toolId,
                        "viewId", "view_abc",
                        "payload", Map.of("params", Map.of("confirmed", true))
                ))
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.requestId").isEqualTo("req_submit_001")
                .jsonPath("$.data.accepted").isEqualTo(true)
                .jsonPath("$.data.runId").isEqualTo(runId)
                .jsonPath("$.data.toolId").isEqualTo(toolId);

        assertThat(pending.block(Duration.ofSeconds(2))).isEqualTo(Map.of("confirmed", true));
    }

    @Test
    void viewportShouldReturnHtmlData() throws Exception {
        Path viewportDir = Path.of(viewportCatalogProperties.getExternalDir());
        Files.createDirectories(viewportDir);
        Files.writeString(viewportDir.resolve("weather_card.html"), "<div>sunny</div>");
        viewportRegistryService.refreshViewports();

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/viewport")
                        .queryParam("viewportKey", "weather_card")
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.data.html").isEqualTo("<div>sunny</div>");
    }

    @Test
    void viewportShouldReturnQlcJsonData() throws Exception {
        Path viewportDir = Path.of(viewportCatalogProperties.getExternalDir());
        Files.createDirectories(viewportDir);
        Files.writeString(viewportDir.resolve("flight_form.qlc"), "{\"schema\":{\"type\":\"object\"},\"packages\":[\"pkg-a\"]}");
        viewportRegistryService.refreshViewports();

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/viewport")
                        .queryParam("viewportKey", "flight_form")
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.data.schema.type").isEqualTo("object")
                .jsonPath("$.data.packages[0]").isEqualTo("pkg-a");
    }

    @Test
    void viewportShouldReturn404WhenMissing() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/viewport")
                        .queryParam("viewportKey", "missing_viewport")
                        .build())
                .exchange()
                .expectStatus().isNotFound()
                .expectBody()
                .jsonPath("$.code").isEqualTo(404);
    }

    @Test
    void chatsAndChatApisShouldReturnStoredChatSnapshot() {
        String message = "0123456789ABCDEFGHI";
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlanExecute",
                        "message", message,
                        "references", List.of(Map.of(
                                "id", "ref_001",
                                "type", "url",
                                "name", "doc",
                                "url", "https://example.com/ref"
                        ))
                ))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));
        assertThat(chunks).isNotNull();

        String joined = String.join("", chunks);
        String chatId = extractFirstValue(joined, "chatId");
        assertThat(chatId).isNotBlank();

        webTestClient.get()
                .uri("/api/chats")
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.data[0].chatId").isEqualTo(chatId)
                .jsonPath("$.data[0].chatName").isEqualTo("0123456789")
                .jsonPath("$.data[0].firstAgentKey").isEqualTo("demoModePlanExecute")
                .jsonPath("$.data[0].createdAt").isNumber()
                .jsonPath("$.data[0].updatedAt").isNumber();

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", chatId)
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.chatId").isEqualTo(chatId)
                .jsonPath("$.data.chatName").isEqualTo("0123456789")
                .jsonPath("$.data.rawMessages").doesNotExist()
                .jsonPath("$.data.events[?(@.type=='request.query')]").exists()
                .jsonPath("$.data.events[?(@.type=='run.start')]").exists()
                .jsonPath("$.data.events[?(@.type=='content.snapshot')]").exists()
                .jsonPath("$.data.events[?(@.type=='run.complete')]").exists();

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", chatId)
                        .queryParam("includeRawMessages", true)
                .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.data.rawMessages[0].role").isEqualTo("user")
                .jsonPath("$.data.events[?(@.type=='request.query')]").exists();
    }

    @Test
    void chatShouldRejectInvalidChatId() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", "not-a-uuid")
                        .build())
                .exchange()
                .expectStatus().isBadRequest();
    }

    @Test
    void chatShouldRejectDeprecatedIncludeEventsParam() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", "123e4567-e89b-12d3-a456-426614174000")
                        .queryParam("includeEvents", true)
                        .build())
                .exchange()
                .expectStatus().isBadRequest()
                .expectBody()
                .jsonPath("$.msg").value(message -> assertThat(String.valueOf(message)).contains("includeEvents is deprecated"));
    }

    @Test
    void chatShouldReturnNotFoundWhenChatMissing() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", "123e4567-e89b-12d3-a456-426614174099")
                        .build())
                .exchange()
                .expectStatus().isNotFound();
    }

    @Test
    void chatDetailShouldContainSingleChatStartAcrossMultipleRuns() {
        FluxExchangeResult<String> firstRun = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlanExecute",
                        "message", "第一轮"
                ))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);
        List<String> firstChunks = firstRun.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));
        assertThat(firstChunks).isNotNull();
        String chatId = extractFirstValue(String.join("", firstChunks), "chatId");
        assertThat(chatId).isNotBlank();

        FluxExchangeResult<String> secondRun = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "chatId", chatId,
                        "agentKey", "demoModePlanExecute",
                        "message", "第二轮"
                ))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);
        List<String> secondChunks = secondRun.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));
        assertThat(secondChunks).isNotNull();

        byte[] responseBody = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", chatId)
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .returnResult()
                .getResponseBody();

        assertThat(responseBody).isNotNull();
        String json = new String(responseBody, StandardCharsets.UTF_8);
        assertThat(countOccurrences(json, "\"type\":\"chat.start\"")).isEqualTo(1);
        assertThat(countOccurrences(json, "\"type\":\"run.start\"")).isGreaterThanOrEqualTo(2);
        assertThat(json).doesNotContain("\"type\":\"chat.update\"");
    }

    @Test
    void chatApiShouldReturnStrictPlanUpdateFormat() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174088";
        Path chatDir = Path.of(chatWindowMemoryProperties.getDir());
        Files.createDirectories(chatDir);

        writeJsonLine(chatDir.resolve("_chats.jsonl"), Map.of(
                "chatId", chatId,
                "chatName", "计划会话",
                "firstAgentKey", "demoModePlanExecute",
                "createdAt", 1707000600000L,
                "updatedAt", 1707000600000L
        ));

        Map<String, Object> run = new LinkedHashMap<>();
        run.put("chatId", chatId);
        run.put("runId", "run_plan_001");
        run.put("transactionId", "run_plan_001");
        run.put("updatedAt", 1707000600000L);
        run.put("query", Map.of(
                "requestId", "req_plan_001",
                "chatId", chatId,
                "role", "user",
                "message", "测试计划",
                "stream", true
        ));
        run.put("messages", List.of(
                Map.of(
                        "role", "user",
                        "content", List.of(Map.of("type", "text", "text", "测试计划")),
                        "ts", 1707000600000L
                ),
                Map.of(
                        "role", "assistant",
                        "content", List.of(Map.of("type", "text", "text", "已创建计划")),
                        "ts", 1707000600001L
                )
        ));
        run.put("planSnapshot", Map.of(
                "planId", "plan_chat_001",
                "plan", List.of(
                        Map.of("taskId", "task0", "description", "收集信息", "status", "init"),
                        Map.of("taskId", "task1", "description", "执行任务", "status", "in_progress")
                )
        ));
        writeJsonLine(chatDir.resolve(chatId + ".json"), run);

        byte[] responseBody = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", chatId)
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .returnResult()
                .getResponseBody();

        assertThat(responseBody).isNotNull();
        Map<String, Object> root = objectMapper.readValue(responseBody, Map.class);
        Map<String, Object> data = (Map<String, Object>) root.get("data");
        List<Map<String, Object>> events = (List<Map<String, Object>>) data.get("events");

        Map<String, Object> planUpdate = events.stream()
                .filter(event -> "plan.update".equals(event.get("type")))
                .findFirst()
                .orElseThrow();

        assertThat(planUpdate).containsEntry("type", "plan.update");
        assertThat(planUpdate).containsEntry("planId", "plan_chat_001");
        assertThat(planUpdate).containsEntry("chatId", chatId);
        assertThat(planUpdate).containsKey("plan");
        assertThat(planUpdate).containsKey("timestamp");
        assertThat(planUpdate).containsEntry("rawEvent", null);
        assertThat(planUpdate).doesNotContainKey("seq");
    }

    @Test
    void queryShouldEmitStrictPlanUpdateSseFormat() throws Exception {
        String chatId = "123e4567-e89b-12d3-a456-426614174089";
        Path chatDir = Path.of(chatWindowMemoryProperties.getDir());
        Files.createDirectories(chatDir);

        writeJsonLine(chatDir.resolve("_chats.jsonl"), Map.of(
                "chatId", chatId,
                "chatName", "计划会话",
                "firstAgentKey", "demoModePlanExecute",
                "createdAt", 1707000700000L,
                "updatedAt", 1707000700000L
        ));

        Map<String, Object> run = new LinkedHashMap<>();
        run.put("chatId", chatId);
        run.put("runId", "run_plan_seed_001");
        run.put("transactionId", "run_plan_seed_001");
        run.put("updatedAt", 1707000700000L);
        run.put("query", Map.of(
                "requestId", "req_plan_seed_001",
                "chatId", chatId,
                "role", "user",
                "message", "初始化计划",
                "stream", true
        ));
        run.put("messages", List.of(
                Map.of(
                        "role", "user",
                        "content", List.of(Map.of("type", "text", "text", "初始化计划")),
                        "ts", 1707000700000L
                ),
                Map.of(
                        "role", "assistant",
                        "content", List.of(Map.of("type", "text", "text", "种子计划")),
                        "ts", 1707000700001L
                )
        ));
        run.put("planSnapshot", Map.of(
                "planId", "plan_chat_seed_001",
                "plan", List.of(
                        Map.of("taskId", "task0", "description", "收集信息", "status", "init"),
                        Map.of("taskId", "task1", "description", "执行任务", "status", "in_progress")
                )
        ));
        writeJsonLine(chatDir.resolve(chatId + ".json"), run);

        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "chatId", chatId,
                        "agentKey", "demoModePlanExecute",
                        "message", "第二轮"
                ))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().contentTypeCompatibleWith(MediaType.TEXT_EVENT_STREAM)
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1600)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(joined).contains("\"type\":\"plan.update\"");
        assertThat(joined).contains("\"rawEvent\":null");

        String planPayload = extractTypedJsonObject(joined, "plan.update");
        assertThat(planPayload).isNotBlank();
        assertThat(planPayload).contains("\"type\":\"plan.update\"");
        assertThat(planPayload).doesNotContain("\"seq\":");
    }

    private String extractFirstValue(String text, String key) {
        Pattern pattern = Pattern.compile("\"" + key + "\":\"([^\"]+)\"");
        Matcher matcher = pattern.matcher(text);
        if (matcher.find()) {
            return matcher.group(1);
        }
        return "";
    }

    private int countOccurrences(String text, String token) {
        int count = 0;
        int start = 0;
        while (true) {
            int index = text.indexOf(token, start);
            if (index < 0) {
                return count;
            }
            count++;
            start = index + token.length();
        }
    }

    private String extractTypedJsonObject(String text, String type) {
        String marker = "{\"type\":\"" + type + "\"";
        int start = text.indexOf(marker);
        if (start < 0) {
            return "";
        }
        int depth = 0;
        for (int i = start; i < text.length(); i++) {
            char ch = text.charAt(i);
            if (ch == '{') {
                depth++;
                continue;
            }
            if (ch == '}') {
                depth--;
                if (depth == 0) {
                    return text.substring(start, i + 1);
                }
            }
        }
        return "";
    }

    private void writeJsonLine(Path path, Object value) throws Exception {
        Files.createDirectories(path.getParent());
        String line = objectMapper.writeValueAsString(value) + System.lineSeparator();
        Files.writeString(path, line, StandardOpenOption.CREATE, StandardOpenOption.APPEND);
    }
}
