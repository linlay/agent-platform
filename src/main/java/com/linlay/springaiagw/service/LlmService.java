package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.config.AgentProviderProperties;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.model.SseChunk;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.ai.chat.messages.UserMessage;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.ai.openai.api.OpenAiApi;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;

import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;

@Service
public class LlmService {

    private static final Logger log = LoggerFactory.getLogger(LlmService.class);

    private final ChatClient bailianChatClient;
    private final ChatClient siliconflowChatClient;
    private final AgentProviderProperties providerProperties;
    private final ObjectMapper objectMapper;

    public record LlmFunctionTool(
            String name,
            String description,
            Map<String, Object> parameters,
            Boolean strict
    ) {
    }

    public record LlmStreamDelta(
            String content,
            List<SseChunk.ToolCall> toolCalls,
            String finishReason
    ) {
    }

    public LlmService(
            ChatClient bailianChatClient,
            ChatClient siliconflowChatClient
    ) {
        this(bailianChatClient, siliconflowChatClient, new AgentProviderProperties(), new ObjectMapper());
    }

    @Autowired
    public LlmService(
            @Qualifier("bailianChatClient") ChatClient bailianChatClient,
            @Qualifier("siliconflowChatClient") ChatClient siliconflowChatClient,
            AgentProviderProperties providerProperties,
            ObjectMapper objectMapper
    ) {
        this.bailianChatClient = bailianChatClient;
        this.siliconflowChatClient = siliconflowChatClient;
        this.providerProperties = providerProperties;
        this.objectMapper = objectMapper;
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
                            log.debug("[{}][{}][delta] content: {}", traceId, stage, chunk);
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
                            log.debug("[{}][{}][delta] content: {}", traceId, stage, chunk);
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

    public Flux<LlmStreamDelta> streamDeltas(
            ProviderType providerType,
            String model,
            String systemPrompt,
            String userPrompt,
            String stage
    ) {
        return streamDeltas(providerType, model, systemPrompt, List.of(), userPrompt, List.of(), stage);
    }

    public Flux<LlmStreamDelta> streamDeltas(
            ProviderType providerType,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage
    ) {
        return streamDeltas(providerType, model, systemPrompt, historyMessages, userPrompt, tools, stage, false);
    }

    public Flux<LlmStreamDelta> streamDeltas(
            ProviderType providerType,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            String stage,
            boolean parallelToolCalls
    ) {
        return Flux.defer(() -> {
            String traceId = "llm-" + UUID.randomUUID().toString().replace("-", "");
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();
            ChatClient chatClient = providerType == ProviderType.SILICONFLOW ? siliconflowChatClient : bailianChatClient;
            boolean hasTools = tools != null && !tools.isEmpty();

            log.info("[{}][{}] LLM delta stream request start provider={}, model={}, tools={}",
                    traceId, stage, providerType, model, tools == null ? 0 : tools.size());
            log.info("[{}][{}] LLM delta stream system prompt:\n{}", traceId, stage, normalizePrompt(systemPrompt));
            log.info("[{}][{}] LLM delta stream history messages count={}", traceId, stage, historyMessages == null ? 0 : historyMessages.size());
            log.info("[{}][{}] LLM delta stream user prompt:\n{}", traceId, stage, normalizePrompt(userPrompt));

            if (chatClient == null) {
                return streamContent(providerType, model, systemPrompt, historyMessages, userPrompt, stage)
                        .map(content -> new LlmStreamDelta(content, null, null));
            }

            Flux<LlmStreamDelta> deltaFlux;
            if (hasTools) {
                AtomicBoolean rawDeltaEmitted = new AtomicBoolean(false);
                deltaFlux = streamDeltasRawSse(providerType, model, systemPrompt, historyMessages, userPrompt, tools, parallelToolCalls)
                        .doOnNext(ignored -> rawDeltaEmitted.set(true))
                        .onErrorResume(ex -> {
                            if (chatClient == null) {
                                return Flux.error(ex);
                            }
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
                            return streamDeltasByChatClient(chatClient, model, systemPrompt, historyMessages, userPrompt, tools, parallelToolCalls);
                        });
            } else {
                if (chatClient == null) {
                    deltaFlux = streamContent(providerType, model, systemPrompt, historyMessages, userPrompt, stage)
                            .map(content -> new LlmStreamDelta(content, null, null));
                } else {
                    deltaFlux = streamDeltasByChatClient(chatClient, model, systemPrompt, historyMessages, userPrompt, tools, false);
                }
            }

            return deltaFlux
                    .filter(delta -> delta != null
                            && ((delta.content() != null && !delta.content().isEmpty())
                            || (delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                            || (delta.finishReason() != null && !delta.finishReason().isBlank())))
                    .doOnNext(delta -> appendDeltaLog(responseBuffer, delta, traceId, stage))
                    .doOnComplete(() -> log.info(
                            "[{}][{}] LLM delta stream response finished in {} ms:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM delta stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> log.warn(
                            "[{}][{}] LLM delta stream canceled in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .timeout(Duration.ofSeconds(60));
        });
    }

    public Flux<String> streamContentRawSse(
            ProviderType providerType,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        return Flux.defer(() -> {
            String traceId = "llm-" + UUID.randomUUID().toString().replace("-", "");
            long startNanos = System.nanoTime();
            StringBuilder responseBuffer = new StringBuilder();

            log.info("[{}][{}] LLM raw SSE content stream request start provider={}, model={}", traceId, stage, providerType, model);
            log.info("[{}][{}] LLM raw SSE content stream system prompt:\n{}", traceId, stage, normalizePrompt(systemPrompt));
            log.info("[{}][{}] LLM raw SSE content stream history messages count={}", traceId, stage, historyMessages == null ? 0 : historyMessages.size());
            log.info("[{}][{}] LLM raw SSE content stream user prompt:\n{}", traceId, stage, normalizePrompt(userPrompt));

            AgentProviderProperties.ProviderConfig config = resolveProviderConfig(providerType);
            WebClient webClient = buildRawWebClient(config);
            Map<String, Object> request = buildRawStreamRequest(model, systemPrompt, historyMessages, userPrompt, List.of(), false);

            return webClient.post()
                    .uri(resolveRawCompletionsUri(config.getBaseUrl()))
                    .accept(MediaType.TEXT_EVENT_STREAM)
                    .bodyValue(request)
                    .retrieve()
                    .bodyToFlux(String.class)
                    .<String>handle((rawChunk, sink) -> {
                        LlmStreamDelta delta = toStreamDeltaFromRawChunk(rawChunk);
                        if (delta != null && delta.content() != null && !delta.content().isEmpty()) {
                            sink.next(delta.content());
                        }
                    })
                    .doOnNext(chunk -> {
                        responseBuffer.append(chunk);
                        log.debug("[{}][{}][delta] content: {}", traceId, stage, chunk);
                    })
                    .doOnComplete(() -> log.info(
                            "[{}][{}] LLM raw SSE content stream finished in {} ms:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer
                    ))
                    .doOnError(ex -> log.error(
                            "[{}][{}] LLM raw SSE content stream failed in {} ms, partial response:\n{}",
                            traceId,
                            stage,
                            elapsedMs(startNanos),
                            responseBuffer,
                            ex
                    ))
                    .doOnCancel(() -> log.warn(
                            "[{}][{}] LLM raw SSE content stream canceled in {} ms, partial response:\n{}",
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

    private Flux<LlmStreamDelta> streamDeltasByChatClient(
            ChatClient chatClient,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            boolean parallelToolCalls
    ) {
        OpenAiChatOptions options = buildStreamOptions(model, tools, parallelToolCalls);
        ChatClient.ChatClientRequestSpec prompt = chatClient.prompt().options(options);
        if (systemPrompt != null && !systemPrompt.isBlank()) {
            prompt = prompt.system(systemPrompt);
        }
        if (historyMessages != null && !historyMessages.isEmpty()) {
            prompt = prompt.messages(historyMessages);
        }
        if (userPrompt != null && !userPrompt.isBlank()) {
            prompt = prompt.user(userPrompt);
        }

        return prompt.stream()
                .chatResponse()
                .map(this::toStreamDelta);
    }

    private Flux<LlmStreamDelta> streamDeltasRawSse(
            ProviderType providerType,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            boolean parallelToolCalls
    ) {
        AgentProviderProperties.ProviderConfig config = resolveProviderConfig(providerType);
        WebClient webClient = buildRawWebClient(config);
        Map<String, Object> request = buildRawStreamRequest(model, systemPrompt, historyMessages, userPrompt, tools, parallelToolCalls);

        return webClient.post()
                .uri(resolveRawCompletionsUri(config.getBaseUrl()))
                .accept(MediaType.TEXT_EVENT_STREAM)
                .bodyValue(request)
                .retrieve()
                .bodyToFlux(String.class)
                .handle((rawChunk, sink) -> {
                    LlmStreamDelta delta = toStreamDeltaFromRawChunk(rawChunk);
                    if (delta != null) {
                        sink.next(delta);
                    }
                });
    }

    private AgentProviderProperties.ProviderConfig resolveProviderConfig(ProviderType providerType) {
        if (providerProperties == null) {
            throw new IllegalStateException("Provider properties not configured");
        }
        if (providerType == ProviderType.SILICONFLOW) {
            return providerProperties.getSiliconflow();
        }
        return providerProperties.getBailian();
    }

    private WebClient buildRawWebClient(AgentProviderProperties.ProviderConfig config) {
        if (config == null) {
            throw new IllegalStateException("Provider config not found");
        }
        return WebClient.builder()
                .baseUrl(config.getBaseUrl())
                .defaultHeader(HttpHeaders.AUTHORIZATION, "Bearer " + config.getApiKey())
                .defaultHeader(HttpHeaders.CONTENT_TYPE, MediaType.APPLICATION_JSON_VALUE)
                .build();
    }

    private String resolveRawCompletionsUri(String baseUrl) {
        String normalized = baseUrl == null ? "" : baseUrl.trim().toLowerCase(Locale.ROOT);
        if (normalized.endsWith("/v1") || normalized.endsWith("/v1/")) {
            return "/chat/completions";
        }
        return "/v1/chat/completions";
    }

    private Map<String, Object> buildRawStreamRequest(
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmFunctionTool> tools,
            boolean parallelToolCalls
    ) {
        Map<String, Object> request = new LinkedHashMap<>();
        request.put("model", model);
        request.put("stream", true);
        request.put("messages", buildRawMessages(systemPrompt, historyMessages, userPrompt));

        List<Map<String, Object>> rawTools = buildRawTools(tools);
        if (!rawTools.isEmpty()) {
            request.put("tools", rawTools);
            request.put("tool_choice", "auto");
            request.put("parallel_tool_calls", parallelToolCalls);
        }
        return request;
    }

    private List<Map<String, Object>> buildRawMessages(
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt
    ) {
        List<Map<String, Object>> messages = new ArrayList<>();
        if (systemPrompt != null && !systemPrompt.isBlank()) {
            messages.add(rawTextMessage("system", systemPrompt));
        }
        if (historyMessages != null && !historyMessages.isEmpty()) {
            for (Message message : historyMessages) {
                messages.addAll(toRawMessages(message));
            }
        }
        if (userPrompt != null && !userPrompt.isBlank()) {
            messages.add(rawTextMessage("user", userPrompt));
        }
        return messages;
    }

    private List<Map<String, Object>> toRawMessages(Message message) {
        if (message == null) {
            return List.of();
        }
        if (message instanceof SystemMessage systemMessage) {
            return List.of(rawTextMessage("system", systemMessage.getText()));
        }
        if (message instanceof UserMessage userMessage) {
            return List.of(rawTextMessage("user", userMessage.getText()));
        }
        if (message instanceof AssistantMessage assistantMessage) {
            Map<String, Object> assistant = new LinkedHashMap<>();
            assistant.put("role", "assistant");
            String content = assistantMessage.getText();
            assistant.put("content", content == null ? "" : content);
            if (assistantMessage.getToolCalls() != null && !assistantMessage.getToolCalls().isEmpty()) {
                List<Map<String, Object>> toolCalls = new ArrayList<>();
                for (AssistantMessage.ToolCall call : assistantMessage.getToolCalls()) {
                    if (call == null) {
                        continue;
                    }
                    Map<String, Object> function = new LinkedHashMap<>();
                    function.put("name", call.name());
                    function.put("arguments", call.arguments());
                    Map<String, Object> toolCall = new LinkedHashMap<>();
                    toolCall.put("id", call.id());
                    toolCall.put("type", call.type() == null ? "function" : call.type());
                    toolCall.put("function", function);
                    toolCalls.add(toolCall);
                }
                if (!toolCalls.isEmpty()) {
                    assistant.put("tool_calls", toolCalls);
                }
            }
            return List.of(assistant);
        }
        if (message instanceof ToolResponseMessage toolResponseMessage) {
            if (toolResponseMessage.getResponses() == null || toolResponseMessage.getResponses().isEmpty()) {
                return List.of();
            }
            List<Map<String, Object>> toolMessages = new ArrayList<>();
            for (ToolResponseMessage.ToolResponse response : toolResponseMessage.getResponses()) {
                if (response == null) {
                    continue;
                }
                Map<String, Object> tool = new LinkedHashMap<>();
                tool.put("role", "tool");
                tool.put("tool_call_id", response.id());
                tool.put("name", response.name());
                tool.put("content", response.responseData());
                toolMessages.add(tool);
            }
            return toolMessages;
        }

        String role = message.getMessageType() == null
                ? "assistant"
                : message.getMessageType().name().toLowerCase(Locale.ROOT);
        return List.of(rawTextMessage(role, message.getText()));
    }

    private Map<String, Object> rawTextMessage(String role, String content) {
        Map<String, Object> message = new LinkedHashMap<>();
        message.put("role", role);
        message.put("content", content == null ? "" : content);
        return message;
    }

    private List<Map<String, Object>> buildRawTools(List<LlmFunctionTool> tools) {
        if (tools == null || tools.isEmpty()) {
            return List.of();
        }
        List<Map<String, Object>> rawTools = new ArrayList<>();
        for (LlmFunctionTool tool : tools) {
            if (tool == null || tool.name() == null || tool.name().isBlank()) {
                continue;
            }
            Map<String, Object> function = new LinkedHashMap<>();
            function.put("name", tool.name());
            if (tool.description() != null && !tool.description().isBlank()) {
                function.put("description", tool.description());
            }
            function.put("parameters", tool.parameters() == null ? Map.of(
                    "type", "object",
                    "properties", Map.of(),
                    "additionalProperties", true
            ) : tool.parameters());
            if (tool.strict() != null) {
                function.put("strict", tool.strict());
            }
            Map<String, Object> toolMap = new LinkedHashMap<>();
            toolMap.put("type", "function");
            toolMap.put("function", function);
            rawTools.add(toolMap);
        }
        return rawTools;
    }

    private LlmStreamDelta toStreamDeltaFromRawChunk(String rawChunk) {
        String payload = normalizeSsePayload(rawChunk);
        if (payload == null) {
            return null;
        }
        try {
            JsonNode root = objectMapper.readTree(payload);
            JsonNode choices = root.path("choices");
            if (!choices.isArray() || choices.isEmpty()) {
                return null;
            }
            JsonNode firstChoice = choices.get(0);
            JsonNode deltaNode = firstChoice.path("delta");

            String content = optionalText(deltaNode.get("content"));
            String finishReason = optionalText(firstChoice.get("finish_reason"));

            List<SseChunk.ToolCall> toolCalls = new ArrayList<>();
            JsonNode toolCallsNode = deltaNode.path("tool_calls");
            if (toolCallsNode.isArray()) {
                for (JsonNode callNode : toolCallsNode) {
                    String callId = optionalText(callNode.get("id"));
                    String type = optionalText(callNode.get("type"));
                    JsonNode functionNode = callNode.path("function");
                    String name = optionalText(functionNode.get("name"));
                    String arguments = optionalText(functionNode.get("arguments"));
                    if (!hasText(callId) && !hasText(name) && !hasText(arguments)) {
                        continue;
                    }
                    toolCalls.add(new SseChunk.ToolCall(
                            callId,
                            hasText(type) ? type : "function",
                            new SseChunk.Function(name, arguments)
                    ));
                }
            }

            return new LlmStreamDelta(content, toolCalls.isEmpty() ? null : toolCalls, finishReason);
        } catch (Exception ex) {
            log.warn("Failed to parse raw SSE chunk: {}", rawChunk, ex);
            return null;
        }
    }

    private String normalizeSsePayload(String rawChunk) {
        if (rawChunk == null || rawChunk.isBlank()) {
            return null;
        }
        String payload = rawChunk.trim();
        if (payload.startsWith("data:")) {
            payload = payload.substring(5).trim();
        }
        if (payload.isBlank() || "[DONE]".equals(payload)) {
            return null;
        }
        return payload;
    }

    private String optionalText(JsonNode node) {
        if (node == null || node.isNull() || node.isMissingNode()) {
            return null;
        }
        if (node.isTextual()) {
            return node.asText();
        }
        return node.toString();
    }

    private boolean hasText(String value) {
        return value != null && !value.isBlank();
    }

    private OpenAiChatOptions buildStreamOptions(String model, List<LlmFunctionTool> tools, boolean parallelToolCalls) {
        OpenAiChatOptions.Builder builder = OpenAiChatOptions.builder()
                .model(model)
                // We consume native tool_calls ourselves. Disable Spring AI internal execution.
                .internalToolExecutionEnabled(false);
        if (tools != null && !tools.isEmpty()) {
            List<OpenAiApi.FunctionTool> functionTools = tools.stream()
                    .filter(tool -> tool != null && tool.name() != null && !tool.name().isBlank())
                    .map(this::toOpenAiFunctionTool)
                    .toList();
            if (!functionTools.isEmpty()) {
                builder.tools(functionTools);
                builder.toolChoice("auto");
                builder.parallelToolCalls(parallelToolCalls);
            }
        }
        return builder.build();
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

    private LlmStreamDelta toStreamDelta(ChatResponse response) {
        if (response == null || response.getResult() == null || response.getResult().getOutput() == null) {
            return new LlmStreamDelta(null, null, null);
        }

        AssistantMessage output = response.getResult().getOutput();
        String content = output.getText();
        List<SseChunk.ToolCall> toolCalls = output.getToolCalls() == null ? List.of()
                : output.getToolCalls().stream()
                .map(call -> new SseChunk.ToolCall(
                        call.id(),
                        call.type(),
                        new SseChunk.Function(call.name(), call.arguments())
                ))
                .toList();
        String finishReason = response.getResult().getMetadata() == null
                ? null
                : response.getResult().getMetadata().getFinishReason();
        return new LlmStreamDelta(content, toolCalls.isEmpty() ? null : toolCalls, finishReason);
    }

    private void appendDeltaLog(StringBuilder buffer, LlmStreamDelta delta, String traceId, String stage) {
        if (delta == null) {
            return;
        }
        if (delta.content() != null && !delta.content().isEmpty()) {
            buffer.append(delta.content());
            log.debug("[{}][{}][delta] content: {}", traceId, stage, delta.content());
        }
        if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
            for (SseChunk.ToolCall call : delta.toolCalls()) {
                if (call == null || call.function() == null) {
                    continue;
                }
                buffer.append("\n[tool_call] id=").append(call.id())
                        .append(", name=").append(call.function().name())
                        .append(", args=").append(call.function().arguments());
                log.debug("[{}][{}][delta] tool_call id={}, name={}, args={}", traceId, stage,
                        call.id(), call.function().name(), call.function().arguments());
            }
        }
        if (delta.finishReason() != null && !delta.finishReason().isBlank()) {
            buffer.append("\n[finish_reason] ").append(delta.finishReason());
            log.debug("[{}][{}][delta] finish_reason={}", traceId, stage, delta.finishReason());
        }
    }

    private long elapsedMs(long startNanos) {
        return (System.nanoTime() - startNanos) / 1_000_000;
    }
}
