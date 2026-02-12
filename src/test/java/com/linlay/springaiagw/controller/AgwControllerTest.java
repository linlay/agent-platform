package com.linlay.springaiagw.controller;

import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.service.LlmService;
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
                "memory.chat.dir=${java.io.tmpdir}/springai-agw-test-chats-${random.uuid}"
        }
)
@AutoConfigureWebTestClient
@Import(AgwControllerTest.TestLlmServiceConfig.class)
class AgwControllerTest {

    @Autowired
    private WebTestClient webTestClient;

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
                .jsonPath("$.data[?(@.key=='demoPlanExecute')]").exists();
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
                .jsonPath("$.data.instructions").isEqualTo("你是高级规划助手。严格使用原生 Function Calling：需要工具时用 tool_calls 顺序执行，不在正文输出工具调用 JSON，最后给简洁总结。");
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
        assertThat(joined).contains("\"type\":\"query.message\"");
        assertThat(joined).contains("\"type\":\"run.start\"");
        assertThat(joined).contains("\"type\":\"run.complete\"");

        String requestId = extractFirstValue(joined, "requestId");
        String runId = extractFirstValue(joined, "runId");
        assertThat(requestId).isNotBlank();
        assertThat(runId).isNotBlank();
        assertThat(requestId).isEqualTo(runId);
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
        assertThat(joined).contains("\"type\":\"query.message\"");
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
        webTestClient.post()
                .uri("/api/submit")
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "requestId", "req_submit_001",
                        "chatId", "123e4567-e89b-12d3-a456-426614174000",
                        "runId", "123e4567-e89b-12d3-a456-426614174001",
                        "toolId", "tool_abc",
                        "viewId", "view_abc",
                        "payload", Map.of("confirmed", true)
                ))
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.requestId").isEqualTo("req_submit_001")
                .jsonPath("$.data.accepted").isEqualTo(true)
                .jsonPath("$.data.runId").isEqualTo("123e4567-e89b-12d3-a456-426614174001")
                .jsonPath("$.data.toolId").isEqualTo("tool_abc");
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
                .expectBody(String.class)
                .value(body -> {
                    assertThat(body).contains("\"chatId\":\"" + chatId + "\"");
                    assertThat(body).contains("\"chatName\":\"0123456789\"");
                    assertThat(body).contains("\"firstAgentKey\":\"demoPlanExecute\"");
                });

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", chatId)
                        .queryParam("includeEvents", true)
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.code").isEqualTo(0)
                .jsonPath("$.msg").isEqualTo("success")
                .jsonPath("$.data.chatId").isEqualTo(chatId)
                .jsonPath("$.data.chatName").isEqualTo("0123456789")
                .jsonPath("$.data.firstAgentKey").doesNotExist()
                .jsonPath("$.data.createdAt").doesNotExist()
                .jsonPath("$.data.messages[0].role").isEqualTo("user")
                .jsonPath("$.data.events[?(@.type=='query.message')]").exists()
                .jsonPath("$.data.events[?(@.type=='run.start')]").exists()
                .jsonPath("$.data.events[?(@.type=='message.snapshot')]").exists()
                .jsonPath("$.data.events[?(@.type=='run.complete')]").exists();
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
    void chatShouldReturnNotFoundWhenChatMissing() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/chat")
                        .queryParam("chatId", "123e4567-e89b-12d3-a456-426614174099")
                        .build())
                .exchange()
                .expectStatus().isNotFound();
    }

    private String extractFirstValue(String text, String key) {
        Pattern pattern = Pattern.compile("\"" + key + "\":\"([^\"]+)\"");
        Matcher matcher = pattern.matcher(text);
        if (matcher.find()) {
            return matcher.group(1);
        }
        return "";
    }
}
