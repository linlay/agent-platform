package com.linlay.springaiagw.service;

import com.linlay.springaiagw.model.ProviderType;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;

import java.time.Duration;
import java.util.List;
import java.util.UUID;

@Service
public class LlmService {

    private static final Logger log = LoggerFactory.getLogger(LlmService.class);

    private final ChatClient bailianChatClient;
    private final ChatClient siliconflowChatClient;

    public LlmService(
            @Qualifier("bailianChatClient") ChatClient bailianChatClient,
            @Qualifier("siliconflowChatClient") ChatClient siliconflowChatClient
    ) {
        this.bailianChatClient = bailianChatClient;
        this.siliconflowChatClient = siliconflowChatClient;
    }

    public Flux<String> streamContent(ProviderType providerType, String model, String userPrompt) {
        return streamContent(providerType, model, null, userPrompt, "default");
    }

    public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
        return streamContent(providerType, model, systemPrompt, userPrompt, "default");
    }

    public Flux<String> streamContent(
            ProviderType providerType,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return Flux.defer(() -> {
            String traceId = "llm-" + UUID.randomUUID().toString().replace("-", "");
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();

            log.info("[{}][{}] LLM stream request start provider={}, model={}", traceId, stage, providerType, model);
            log.info("[{}][{}] LLM stream system prompt:\n{}", traceId, stage, normalizePrompt(systemPrompt));
            log.info("[{}][{}] LLM stream user prompt:\n{}", traceId, stage, normalizePrompt(userPrompt));

            ChatClient chatClient = providerType == ProviderType.SILICONFLOW ? siliconflowChatClient : bailianChatClient;
            OpenAiChatOptions options = OpenAiChatOptions.builder().model(model).build();
            ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
            if (systemPrompt != null && !systemPrompt.isBlank()) {
                prompt = prompt.system(systemPrompt);
            }

            return prompt.user(userPrompt)
                    .stream()
                    .content()
                    .doOnNext(chunk -> {
                        if (chunk != null) {
                            responseBuffer.append(chunk);
                        }
                    })
                    .doOnComplete(() -> log.info(
                            "[{}][{}] LLM stream response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> log.warn(
                            "[{}][{}] LLM stream canceled in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .timeout(Duration.ofSeconds(60));
        });
    }

    public Flux<String> streamContent(
            ProviderType providerType,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        ChatClient chatClient = providerType == ProviderType.SILICONFLOW ? siliconflowChatClient : bailianChatClient;
        if (historyMessages == null || historyMessages.isEmpty() || chatClient == null) {
            return streamContent(providerType, model, systemPrompt, userPrompt, stage);
        }

        return Flux.defer(() -> {
            String traceId = "llm-" + UUID.randomUUID().toString().replace("-", "");
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();

            log.info("[{}][{}] LLM stream request start provider={}, model={}", traceId, stage, providerType, model);
            log.info("[{}][{}] LLM stream system prompt:\n{}", traceId, stage, normalizePrompt(systemPrompt));
            log.info("[{}][{}] LLM stream history messages count={}", traceId, stage, historyMessages.size());
            log.info("[{}][{}] LLM stream user prompt:\n{}", traceId, stage, normalizePrompt(userPrompt));

            OpenAiChatOptions options = OpenAiChatOptions.builder().model(model).build();
            ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
            if (systemPrompt != null && !systemPrompt.isBlank()) {
                prompt = prompt.system(systemPrompt);
            }
            prompt = prompt.messages(historyMessages);

            return prompt.user(userPrompt)
                    .stream()
                    .content()
                    .doOnNext(chunk -> {
                        if (chunk != null) {
                            responseBuffer.append(chunk);
                        }
                    })
                    .doOnComplete(() -> log.info(
                            "[{}][{}] LLM stream response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> log.warn(
                            "[{}][{}] LLM stream canceled in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .timeout(Duration.ofSeconds(60));
        });
    }

    public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
        return completeText(providerType, model, systemPrompt, userPrompt, "default");
    }

    public Mono<String> completeText(
            ProviderType providerType,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return Mono.fromCallable(() -> {
                    String traceId = "llm-" + UUID.randomUUID().toString().replace("-", "");
                    long startNanos = System.nanoTime();

                    log.info("[{}][{}] LLM call request start provider={}, model={}", traceId, stage, providerType, model);
                    log.info("[{}][{}] LLM call system prompt:\n{}", traceId, stage, normalizePrompt(systemPrompt));
                    log.info("[{}][{}] LLM call user prompt:\n{}", traceId, stage, normalizePrompt(userPrompt));

                    ChatClient chatClient = providerType == ProviderType.SILICONFLOW ? siliconflowChatClient : bailianChatClient;
                    OpenAiChatOptions options = OpenAiChatOptions.builder().model(model).build();
                    ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
                    if (systemPrompt != null && !systemPrompt.isBlank()) {
                        prompt = prompt.system(systemPrompt);
                    }

                    String content = prompt.user(userPrompt)
                            .call()
                            .content();
                    String normalized = content == null ? "" : content;

                    log.info(
                            "[{}][{}] LLM call response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            normalized
                    );
                    return normalized;
                })
                .doOnError(ex -> log.error("LLM call failed provider={}, stage={}", providerType, stage, ex))
                .subscribeOn(Schedulers.boundedElastic())
                .onErrorReturn("");
    }

    private String normalizePrompt(String prompt) {
        return prompt == null ? "" : prompt;
    }

    private long elapsedMs(long startNanos) {
        return (System.nanoTime() - startNanos) / 1_000_000;
    }
}
