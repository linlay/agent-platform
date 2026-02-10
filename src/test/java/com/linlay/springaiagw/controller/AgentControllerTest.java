package com.linlay.springaiagw.controller;

import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.service.LlmService;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.reactive.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Primary;
import org.springframework.http.MediaType;
import org.springframework.test.web.reactive.server.FluxExchangeResult;
import org.springframework.test.web.reactive.server.WebTestClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.time.Duration;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;
import org.springframework.context.annotation.Import;

@SpringBootTest(
        webEnvironment = SpringBootTest.WebEnvironment.MOCK,
        properties = {
                "agent.providers.bailian.base-url=https://example.com/v1",
                "agent.providers.bailian.api-key=test-bailian-key",
                "agent.providers.bailian.model=test-bailian-model",
                "agent.providers.siliconflow.base-url=https://example.com/v1",
                "agent.providers.siliconflow.api-key=test-siliconflow-key",
                "agent.providers.siliconflow.model=test-siliconflow-model"
        }
)
@AutoConfigureWebTestClient
@Import(AgentControllerTest.TestLlmServiceConfig.class)
class AgentControllerTest {

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
    void weatherUseCaseShouldContainThinkingAndDoneEvent() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/agent/demoPlanExecute")
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.TEXT_EVENT_STREAM)
                .bodyValue(Map.of(
                        "message", "查询上海天气",
                        "city", "Shanghai"
                ))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1200)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(joined).contains("thinking");
        assertThat(joined).contains("content");
        assertThat(joined).contains("[DONE]");
    }

    @Test
    void reactModeShouldStreamFinalAnswerChunks() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/agent/demoReAct")
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.TEXT_EVENT_STREAM)
                .bodyValue(Map.of("message", "写一首200字的散文"))
                .exchange()
                .expectStatus().isOk()
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1200)
                .collectList()
                .block(Duration.ofSeconds(8));

        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);
        assertThat(joined).contains("\"content\":\"这是\"");
        assertThat(joined).contains("\"content\":\"测试\"");
        assertThat(joined).contains("\"content\":\"输出\"");
        assertThat(joined).doesNotContain("\"content\":\"这是测试输出\"");
    }

    @Test
    void agwWeatherUseCaseShouldContainAgwStandardEvents() {
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/agw-agent/demoPlanExecute")
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.TEXT_EVENT_STREAM)
                .bodyValue(Map.of(
                        "message", "查询上海天气",
                        "city", "Shanghai",
                        "chatId", "chat_123",
                        "requestId", "req_001"
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
        assertThat(joined).contains("\"type\":\"chat.start\"");
        assertThat(joined).contains("\"type\":\"run.start\"");
        assertThat(joined).contains("\"type\":\"reasoning.start\"");
        assertThat(joined).contains("\"type\":\"content.start\"");
        assertThat(joined).contains("\"type\":\"message.start\"");
        assertThat(joined).contains("\"type\":\"message.delta\"");
        assertThat(joined).contains("\"type\":\"message.end\"");
        assertThat(joined).contains("\"type\":\"run.complete\"");

        int queryMessageIndex = joined.indexOf("\"type\":\"query.message\"");
        int chatStartIndex = joined.indexOf("\"type\":\"chat.start\"");
        int runStartIndex = joined.indexOf("\"type\":\"run.start\"");
        assertThat(queryMessageIndex).isGreaterThanOrEqualTo(0);
        assertThat(chatStartIndex).isGreaterThan(queryMessageIndex);
        assertThat(runStartIndex).isGreaterThan(chatStartIndex);
    }
}
