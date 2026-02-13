package com.linlay.springaiagw.controller;

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
import java.time.Duration;
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
                .jsonPath("$.data[?(@.key=='demoPlanExecute')]").exists()
                .jsonPath("$.data[?(@.key=='demoViewport')]").exists()
                .jsonPath("$.data[?(@.key=='demoAction')]").exists();
    }

    @Test
    void agentShouldReturnDetailByAgentKey() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path("/api/agent").queryParam("agentKey", "demoPlanExecute").build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.key").isEqualTo("demoPlanExecute")
                .jsonPath("$.data.meta.providerType").isEqualTo("BAILIAN")
                .jsonPath("$.data.instructions").isEqualTo("你是高级执行助手。基于步骤上下文执行工具并产出步骤结果。");
    }

    @Test
    void queryShouldStreamByDefaultWithoutAcceptOrStreamParam() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/query")
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoPlanExecute",
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
                        "agentKey", "demoPlain",
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
                        "agentKey", "demoPlanExecute",
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
                        "agentKey", "demoPlanExecute",
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
                        "agentKey", "demoPlanExecute",
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
                .jsonPath("$.data[0].firstAgentKey").isEqualTo("demoPlanExecute")
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
                        "agentKey", "demoPlanExecute",
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
                        "agentKey", "demoPlanExecute",
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
}
