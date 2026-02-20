package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.config.AgentProviderProperties;
import com.linlay.springaiagw.config.ChatClientRegistry;
import com.linlay.springaiagw.config.LlmInteractionLogProperties;
import com.linlay.springaiagw.model.ProviderProtocol;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.ai.openai.api.OpenAiApi;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;
import reactor.netty.resources.ConnectionProvider;

import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.atomic.AtomicBoolean;

@Service
public class LlmService {

    private static final Logger log = LoggerFactory.getLogger(LlmService.class);
    private static final long DEFAULT_STREAM_TIMEOUT_MS = 60_000L;

    private final ChatClientRegistry chatClientRegistry;
    private final AgentProviderProperties providerProperties;
    private final OpenAiCompatibleSseClient openAiCompatibleSseClient;
    private final AnthropicSseClient anthropicSseClient;
    private final LlmCallLogger callLogger;
    private final Map<String, ChatClient> legacyClients;

    public record LlmFunctionTool(
            String name,
            String description,
            Map<String, Object> parameters,
            Boolean strict
    ) {
    }

    public LlmService(ChatClientRegistry chatClientRegistry) {
        this(
                chatClientRegistry,
                new AgentProviderProperties(),
                new ObjectMapper(),
                new LlmInteractionLogProperties(),
                null,
                Map.of()
        );
    }

    @Autowired
    public LlmService(
            ChatClientRegistry chatClientRegistry,
            AgentProviderProperties providerProperties,
            ObjectMapper objectMapper,
            LlmInteractionLogProperties logProperties,
            ConnectionProvider llmConnectionProvider
    ) {
        this(chatClientRegistry, providerProperties, objectMapper, logProperties, llmConnectionProvider, Map.of());
    }

    /**
     * Backward-compatible constructor for tests that historically injected two ChatClient instances.
     */
    public LlmService(ChatClient bailianChatClient, ChatClient siliconflowChatClient) {
        this(
                null,
                new AgentProviderProperties(),
                new ObjectMapper(),
                new LlmInteractionLogProperties(),
                null,
                legacyClientMap(bailianChatClient, siliconflowChatClient)
        );
    }

    private LlmService(
            ChatClientRegistry chatClientRegistry,
            AgentProviderProperties providerProperties,
            ObjectMapper objectMapper,
            LlmInteractionLogProperties logProperties,
            ConnectionProvider connectionProvider,
            Map<String, ChatClient> legacyClients
    ) {
        this.chatClientRegistry = chatClientRegistry;
        this.providerProperties = providerProperties == null ? new AgentProviderProperties() : providerProperties;
        this.callLogger = new LlmCallLogger(logProperties);
        this.openAiCompatibleSseClient = new OpenAiCompatibleSseClient(this.providerProperties, objectMapper, this.callLogger, connectionProvider);
        this.anthropicSseClient = new AnthropicSseClient();
        this.legacyClients = legacyClients == null ? Map.of() : Map.copyOf(legacyClients);
    }

    public Flux<String> streamContent(String providerKey, String model, String userPrompt) {
        return streamContent(providerKey, model, null, userPrompt, "default");
    }

    public Flux<String> streamContent(LlmCallSpec spec) {
        if (spec == null) {
            return Flux.error(new IllegalArgumentException("spec must not be null"));
        }
        return streamDeltas(spec)
                .mapNotNull(LlmDelta::content)
                .filter(StringUtils::hasText);
    }

    public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt) {
        return streamContent(providerKey, model, systemPrompt, userPrompt, "default");
    }

    public Flux<String> streamContent(
            String providerKey,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return streamContentInternal(providerKey, model, systemPrompt, List.of(), userPrompt, stage);
    }

    public Flux<String> streamContent(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        return streamContentInternal(providerKey, model, systemPrompt, historyMessages, userPrompt, stage);
    }

    public Flux<LlmDelta> streamDeltas(
            String providerKey,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return streamDeltas(providerKey, model, systemPrompt, List.of(), userPrompt, List.of(), stage);
    }

    public Flux<LlmDelta> streamDeltas(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage
    ) {
        return streamDeltas(providerKey, model, systemPrompt, historyMessages, userPrompt, tools, stage, false);
    }

    public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
        if (spec == null) {
            return Flux.error(new IllegalArgumentException("spec must not be null"));
        }
        return streamDeltas(
                spec.providerKey(),
                spec.model(),
                spec.systemPrompt(),
                spec.messages(),
                spec.userPrompt(),
                spec.tools(),
                spec.stage(),
                spec.parallelToolCalls(),
                spec.toolChoice(),
                spec.jsonSchema(),
                spec.compute(),
                spec.reasoningEnabled(),
                spec.maxTokens(),
                spec.timeoutMs()
        );
    }

    public Flux<LlmDelta> streamDeltas(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage,
            boolean parallelToolCalls
    ) {
        return streamDeltas(
                providerKey,
                model,
                systemPrompt,
                historyMessages,
                userPrompt,
                tools,
                stage,
                parallelToolCalls,
                ToolChoice.AUTO,
                null,
                ComputePolicy.MEDIUM,
                false,
                null,
                null
        );
    }

    public Flux<LlmDelta> streamDeltas(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage,
            boolean parallelToolCalls,
            ToolChoice toolChoice,
            String jsonSchema,
            ComputePolicy computePolicy,
            boolean reasoningEnabled,
            Integer maxTokens,
            Long timeoutMs
    ) {
        return streamDeltasInternal(providerKey, model, systemPrompt, historyMessages, userPrompt, tools, stage,
                parallelToolCalls, toolChoice, jsonSchema, computePolicy, reasoningEnabled, maxTokens, timeoutMs);
    }

    public Flux<String> streamContentRawSse(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        ProviderProtocol protocol = resolveProviderProtocol(providerKey);
        if (protocol == ProviderProtocol.ANTHROPIC) {
            return anthropicSseClient.streamContentRawSse(providerKey, model, systemPrompt, historyMessages, userPrompt, stage);
        }
        return openAiCompatibleSseClient.streamContentRawSse(providerKey, model, systemPrompt, historyMessages, userPrompt, stage);
    }

    public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt) {
        return completeText(providerKey, model, systemPrompt, userPrompt, "default");
    }

    public Mono<String> completeText(
            String providerKey,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return completeTextInternal(providerKey, model, systemPrompt, userPrompt, stage);
    }

    private Flux<String> streamContentInternal(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        ProviderProtocol protocol = resolveProviderProtocol(providerKey);
        if (protocol == ProviderProtocol.ANTHROPIC) {
            return anthropicSseClient.streamContentRawSse(providerKey, model, systemPrompt, historyMessages, userPrompt, stage);
        }
        ChatClient chatClient = resolveChatClient(providerKey);
        if (chatClient == null) {
            return Flux.error(new IllegalStateException("No ChatClient registered for provider key: " + providerKey));
        }

        return Flux.defer(() -> {
            String traceId = callLogger.generateTraceId();
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();

            callLogger.info(log, "[{}][{}] LLM stream request start provider={}, model={}", traceId, stage, providerKey, model);
            callLogger.info(log, "[{}][{}] LLM stream request body:\n{}", traceId, stage, safeJson(openAiCompatibleSseClient.buildRequestBody(
                    providerKey,
                    model,
                    systemPrompt,
                    historyMessages,
                    userPrompt,
                    List.of(),
                    false,
                    ToolChoice.NONE,
                    null,
                    ComputePolicy.MEDIUM,
                    false,
                    null
            )));
            callLogger.info(log, "[{}][{}] LLM stream system prompt:\n{}", traceId, stage, callLogger.normalizePrompt(systemPrompt));
            callLogger.info(log, "[{}][{}] LLM stream history messages count={}", traceId, stage, historyMessages == null ? 0 : historyMessages.size());
            callLogger.logHistoryMessages(log, traceId, stage, historyMessages);
            callLogger.info(log, "[{}][{}] LLM stream user prompt:\n{}", traceId, stage, callLogger.normalizePrompt(userPrompt));

            OpenAiChatOptions options = OpenAiChatOptions.builder().model(model).build();
            ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
            if (StringUtils.hasText(systemPrompt)) {
                prompt = prompt.system(systemPrompt);
            }
            if (historyMessages != null && !historyMessages.isEmpty()) {
                prompt = prompt.messages(historyMessages);
            }

            return prompt.user(userPrompt)
                    .stream()
                    .content()
                    .doOnNext(chunk -> {
                        if (chunk != null) {
                            String safeChunk = callLogger.sanitizeText(chunk);
                            responseBuffer.append(safeChunk);
                            callLogger.debug(log, "[{}][{}][delta] content: {}", traceId, stage, safeChunk);
                        }
                    })
                    .doOnComplete(() -> callLogger.info(
                            log,
                            "[{}][{}] LLM stream response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> callLogger.info(
                            log,
                            "[{}][{}] LLM stream canceled in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .timeout(Duration.ofMillis(resolveTimeoutMs(null)));
        });
    }

    private Flux<LlmDelta> streamDeltasInternal(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage,
            boolean parallelToolCalls,
            ToolChoice toolChoice,
            String jsonSchema,
            ComputePolicy computePolicy,
            boolean reasoningEnabled,
            Integer maxTokens,
            Long timeoutMs
    ) {
        return Flux.defer(() -> {
            String traceId = callLogger.generateTraceId();
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();
            ChatClient chatClient = resolveChatClient(providerKey);
            boolean hasTools = tools != null && !tools.isEmpty();
            ProviderProtocol protocol = resolveProviderProtocol(providerKey);
            boolean preferRawSse = requiresRawSse(jsonSchema, computePolicy, reasoningEnabled);
            Map<String, Object> requestBody = openAiCompatibleSseClient.buildRequestBody(
                    providerKey,
                    model,
                    systemPrompt,
                    historyMessages,
                    userPrompt,
                    tools,
                    parallelToolCalls,
                    toolChoice,
                    jsonSchema,
                    computePolicy,
                    reasoningEnabled,
                    maxTokens
            );

            callLogger.info(log, "[{}][{}] LLM delta stream request start provider={}, model={}, tools={}",
                    traceId, stage, providerKey, model, hasTools ? tools.size() : 0);
            callLogger.info(log, "[{}][{}] LLM delta stream request body:\n{}",
                    traceId, stage, safeJson(requestBody));
            callLogger.info(log, "[{}][{}] LLM delta stream system prompt:\n{}", traceId, stage, callLogger.normalizePrompt(systemPrompt));
            callLogger.info(log, "[{}][{}] LLM delta stream history messages count={}", traceId, stage, historyMessages == null ? 0 : historyMessages.size());
            callLogger.logHistoryMessages(log, traceId, stage, historyMessages);
            callLogger.info(log, "[{}][{}] LLM delta stream user prompt:\n{}", traceId, stage, callLogger.normalizePrompt(userPrompt));

            if (chatClient == null) {
                // Compatibility bridge for tests/legacy callers overriding streamContent.
                return streamContent(providerKey, model, systemPrompt, userPrompt, stage)
                        .map(content -> new LlmDelta(content, null, null));
            }

            Flux<LlmDelta> deltaFlux;
            if (protocol == ProviderProtocol.ANTHROPIC) {
                deltaFlux = anthropicSseClient.streamDeltasRawSse(
                        providerKey,
                        model,
                        systemPrompt,
                        historyMessages,
                        userPrompt,
                        tools,
                        parallelToolCalls,
                        toolChoice,
                        jsonSchema,
                        computePolicy,
                        reasoningEnabled,
                        maxTokens,
                        traceId,
                        stage
                );
            } else if (hasTools || preferRawSse) {
                AtomicBoolean rawDeltaEmitted = new AtomicBoolean(false);
                deltaFlux = openAiCompatibleSseClient.streamDeltasRawSse(
                                providerKey,
                                model,
                                systemPrompt,
                                historyMessages,
                                userPrompt,
                                tools,
                                parallelToolCalls,
                                toolChoice,
                                jsonSchema,
                                computePolicy,
                                reasoningEnabled,
                                maxTokens,
                                traceId,
                                stage
                        )
                        .doOnNext(ignored -> rawDeltaEmitted.set(true))
                        .onErrorResume(ex -> {
                            if (rawDeltaEmitted.get()) {
                                log.warn(
                                        "[{}][{}] raw delta stream failed after partial output, skip fallback to avoid duplicate stream events",
                                        traceId,
                                        stage,
                                        ex
                                );
                                return Flux.error(ex);
                            }
                            log.warn("[{}][{}] raw delta stream failed, fallback to ChatClient stream", traceId, stage, ex);
                            return streamDeltasByChatClient(
                                    chatClient,
                                    model,
                                    systemPrompt,
                                    historyMessages,
                                    userPrompt,
                                    tools,
                                    parallelToolCalls,
                                    toolChoice,
                                    maxTokens
                            );
                        });
            } else {
                deltaFlux = streamDeltasByChatClient(
                        chatClient,
                        model,
                        systemPrompt,
                        historyMessages,
                        userPrompt,
                        tools,
                        false,
                        toolChoice,
                        maxTokens
                );
            }

            return deltaFlux
                    .filter(delta -> delta != null
                            && (StringUtils.hasText(delta.reasoning())
                            || (StringUtils.hasText(delta.content()))
                            || (delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                            || StringUtils.hasText(delta.finishReason())
                            || (delta.usage() != null && !delta.usage().isEmpty())))
                    .doOnNext(delta -> callLogger.appendDeltaLog(responseBuffer, delta, traceId, stage))
                    .doOnComplete(() -> callLogger.info(
                            log,
                            "[{}][{}] LLM delta stream response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM delta stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> callLogger.info(
                            log,
                            "[{}][{}] LLM delta stream canceled in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .timeout(Duration.ofMillis(resolveTimeoutMs(timeoutMs)));
        });
    }

    private long resolveTimeoutMs(Long timeoutMs) {
        if (timeoutMs == null || timeoutMs <= 0) {
            return DEFAULT_STREAM_TIMEOUT_MS;
        }
        return timeoutMs;
    }

    private Mono<String> completeTextInternal(
            String providerKey,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        ProviderProtocol protocol = resolveProviderProtocol(providerKey);
        if (protocol == ProviderProtocol.ANTHROPIC) {
            return Mono.error(new UnsupportedOperationException(
                    "Anthropic protocol is not implemented yet for provider: " + providerKey
            ));
        }
        return Mono.fromCallable(() -> {
                    ChatClient chatClient = resolveChatClient(providerKey);
                    if (chatClient == null) {
                        throw new IllegalStateException("No ChatClient registered for provider key: " + providerKey);
                    }
                    String traceId = callLogger.generateTraceId();
                    long startNanos = System.nanoTime();

                    callLogger.info(log, "[{}][{}] LLM call request start provider={}, model={}", traceId, stage, providerKey, model);
                    callLogger.info(log, "[{}][{}] LLM call request body:\n{}", traceId, stage, safeJson(openAiCompatibleSseClient.buildRequestBody(
                            providerKey,
                            model,
                            systemPrompt,
                            List.of(),
                            userPrompt,
                            List.of(),
                            false,
                            ToolChoice.NONE,
                            null,
                            ComputePolicy.MEDIUM,
                            false,
                            null
                    )));
                    callLogger.info(log, "[{}][{}] LLM call system prompt:\n{}", traceId, stage, callLogger.normalizePrompt(systemPrompt));
                    callLogger.info(log, "[{}][{}] LLM call user prompt:\n{}", traceId, stage, callLogger.normalizePrompt(userPrompt));

                    OpenAiChatOptions options = OpenAiChatOptions.builder().model(model).build();
                    ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
                    if (StringUtils.hasText(systemPrompt)) {
                        prompt = prompt.system(systemPrompt);
                    }

                    String content = prompt.user(userPrompt)
                            .call()
                            .content();
                    String normalized = content == null ? "" : content;

                    callLogger.info(
                            log,
                            "[{}][{}] LLM call response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            callLogger.elapsedMs(startNanos),
                            callLogger.sanitizeText(normalized)
                    );
                    return normalized;
                })
                .doOnError(ex -> log.error("LLM call failed provider={}, stage={}", providerKey, stage, ex))
                .subscribeOn(Schedulers.boundedElastic())
                .onErrorReturn("");
    }

    private Flux<LlmDelta> streamDeltasByChatClient(
            ChatClient chatClient,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            boolean parallelToolCalls,
            ToolChoice toolChoice,
            Integer maxTokens
    ) {
        OpenAiChatOptions options = buildStreamOptions(model, tools, parallelToolCalls, toolChoice, maxTokens);
        ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
        if (StringUtils.hasText(systemPrompt)) {
            prompt = prompt.system(systemPrompt);
        }
        if (historyMessages != null && !historyMessages.isEmpty()) {
            prompt = prompt.messages(historyMessages);
        }
        if (StringUtils.hasText(userPrompt)) {
            prompt = prompt.user(userPrompt);
        }

        return prompt.stream()
                .chatResponse()
                .map(this::toStreamDelta);
    }

    private OpenAiChatOptions buildStreamOptions(
            String model,
            List<LlmFunctionTool> tools,
            boolean parallelToolCalls,
            ToolChoice toolChoice,
            Integer maxTokens
    ) {
        OpenAiChatOptions.Builder builder = OpenAiChatOptions.builder()
                .model(model)
                // We consume native tool_calls ourselves. Disable Spring AI internal execution.
                .internalToolExecutionEnabled(false);
        if (maxTokens != null && maxTokens > 0) {
            builder.maxTokens(maxTokens);
        }
        if (tools != null && !tools.isEmpty() && toolChoice != ToolChoice.NONE) {
            List<OpenAiApi.FunctionTool> functionTools = tools.stream()
                    .filter(tool -> tool != null && StringUtils.hasText(tool.name()))
                    .map(this::toOpenAiFunctionTool)
                    .toList();
            if (!functionTools.isEmpty()) {
                builder.tools(functionTools);
                builder.toolChoice(toToolChoiceValue(toolChoice));
                builder.parallelToolCalls(parallelToolCalls);
            }
        }
        return builder.build();
    }

    private boolean requiresRawSse(String jsonSchema, ComputePolicy computePolicy, boolean reasoningEnabled) {
        if (StringUtils.hasText(jsonSchema)) {
            return true;
        }
        if (reasoningEnabled) {
            return true;
        }
        return computePolicy != null && computePolicy != ComputePolicy.MEDIUM;
    }

    private String toToolChoiceValue(ToolChoice toolChoice) {
        if (toolChoice == null) {
            return "auto";
        }
        return switch (toolChoice) {
            case NONE -> "none";
            case REQUIRED -> "required";
            case AUTO -> "auto";
        };
    }

    private OpenAiApi.FunctionTool toOpenAiFunctionTool(LlmFunctionTool tool) {
        Map<String, Object> parameters = tool.parameters() == null ? Map.of(
                "type", "object",
                "properties", Map.of(),
                "additionalProperties", true
        ) : tool.parameters();
        OpenAiApi.FunctionTool.Function function = new OpenAiApi.FunctionTool.Function(
                tool.description(),
                tool.name(),
                parameters,
                tool.strict()
        );
        return new OpenAiApi.FunctionTool(function);
    }

    private LlmDelta toStreamDelta(ChatResponse response) {
        if (response == null || response.getResult() == null || response.getResult().getOutput() == null) {
            return new LlmDelta(null, null, null);
        }

        AssistantMessage output = response.getResult().getOutput();
        String content = output.getText();
        List<ToolCallDelta> toolCalls = output.getToolCalls() == null ? List.of()
                : output.getToolCalls().stream()
                .map(call -> new ToolCallDelta(
                        call.id(),
                        call.type(),
                        call.name(),
                        call.arguments()
                ))
                .toList();
        String finishReason = response.getResult().getMetadata() == null
                ? null
                : response.getResult().getMetadata().getFinishReason();
        return new LlmDelta(content, toolCalls.isEmpty() ? null : toolCalls, finishReason);
    }

    private ProviderProtocol resolveProviderProtocol(String providerKey) {
        if (providerProperties == null) {
            return ProviderProtocol.OPENAI_COMPATIBLE;
        }
        AgentProviderProperties.ProviderConfig config = providerProperties.getProvider(providerKey);
        if (config == null || config.getProtocol() == null) {
            return ProviderProtocol.OPENAI_COMPATIBLE;
        }
        return config.getProtocol();
    }

    private ChatClient resolveChatClient(String providerKey) {
        if (chatClientRegistry != null) {
            ChatClient resolved = chatClientRegistry.getClient(providerKey);
            if (resolved != null) {
                return resolved;
            }
        }
        if (legacyClients.isEmpty()) {
            return null;
        }
        String normalized = StringUtils.hasText(providerKey)
                ? providerKey.trim().toLowerCase(Locale.ROOT)
                : "bailian";
        ChatClient legacy = legacyClients.get(normalized);
        if (legacy != null) {
            return legacy;
        }
        return legacyClients.get("bailian");
    }

    private static Map<String, ChatClient> legacyClientMap(ChatClient bailianChatClient, ChatClient siliconflowChatClient) {
        Map<String, ChatClient> clients = new LinkedHashMap<>();
        if (bailianChatClient != null) {
            clients.put("bailian", bailianChatClient);
        }
        if (siliconflowChatClient != null) {
            clients.put("siliconflow", siliconflowChatClient);
        }
        return Map.copyOf(clients);
    }

    private String safeJson(Object value) {
        try {
            return callLogger.sanitizeText(new ObjectMapper().writeValueAsString(value));
        } catch (Exception ex) {
            return callLogger.sanitizeText(String.valueOf(value));
        }
    }
}
