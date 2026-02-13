package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.agw.AgentDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.springframework.ai.chat.messages.Message;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public class DefinitionDrivenAgent implements Agent {

    private static final Logger log = LoggerFactory.getLogger(DefinitionDrivenAgent.class);
    private static final int MAX_TOOL_CALLS = 6;
    private static final int MAX_REACT_STEPS = 6;
    private static final Pattern VIEWPORT_MAPPING_PATTERN = Pattern.compile(
            "type\\s*=\\s*([a-zA-Z0-9_]+)\\s*[,，]\\s*key\\s*=\\s*([a-zA-Z0-9_\\-]+)",
            Pattern.CASE_INSENSITIVE
    );

    private final AgentDefinition definition;
    private final LlmService llmService;
    @SuppressWarnings("unused")
    private final DeltaStreamService deltaStreamService;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryStore chatWindowMemoryStore;
    private final Map<String, BaseTool> enabledToolsByName;
    private final DecisionChunkHandler decisionChunkHandler;
    private final DecisionParser decisionParser;
    private final ToolArgumentResolver toolArgumentResolver;
    private final AgentPromptBuilder promptBuilder;
    private final ToolExecutor toolExecutor;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper
    ) {
        this(definition, llmService, deltaStreamService, toolRegistry, objectMapper, null, null);
    }

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore
    ) {
        this(definition, llmService, deltaStreamService, toolRegistry, objectMapper, chatWindowMemoryStore, null);
    }

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore,
            FrontendSubmitCoordinator frontendSubmitCoordinator
    ) {
        this.definition = definition;
        this.llmService = llmService;
        this.deltaStreamService = deltaStreamService;
        this.toolRegistry = toolRegistry;
        this.objectMapper = objectMapper;
        this.chatWindowMemoryStore = chatWindowMemoryStore;
        this.enabledToolsByName = resolveEnabledTools(definition.tools());
        this.decisionChunkHandler = new DecisionChunkHandler(objectMapper, definition.id(), toolRegistry::toolCallType);
        this.decisionParser = new DecisionParser(objectMapper);
        this.toolArgumentResolver = new ToolArgumentResolver(objectMapper);
        this.promptBuilder = new AgentPromptBuilder(objectMapper, definition.systemPrompt(), this.enabledToolsByName);
        this.toolExecutor = new ToolExecutor(
                toolRegistry,
                this.toolArgumentResolver,
                objectMapper,
                this.promptBuilder,
                this.enabledToolsByName,
                definition.id(),
                frontendSubmitCoordinator
        );
    }

    @Override
    public String id() {
        return definition.id();
    }

    @Override
    public String description() {
        return definition.description();
    }

    @Override
    public String providerKey() {
        return definition.providerKey();
    }

    @Override
    public String model() {
        return definition.model();
    }

    @Override
    public String systemPrompt() {
        return definition.systemPrompt();
    }

    @Override
    public AgentMode mode() {
        return definition.mode();
    }

    @Override
    public List<String> tools() {
        return definition.tools();
    }

    @Override
    public Flux<AgentDelta> stream(AgentRequest request) {
        log.info(
                "[agent:{}] stream start provider={}, model={}, mode={}, tools={}, message={}",
                id(),
                providerKey(),
                model(),
                definition.mode(),
                enabledToolsByName.keySet(),
                normalize(request.message(), "")
        );

        List<Message> historyMessages = loadHistoryMessages(request.chatId());
        TurnTrace trace = new TurnTrace();

        Flux<AgentDelta> flow = switch (definition.mode()) {
            case PLAIN -> plainContent(request, historyMessages);
            case RE_ACT -> reactFlow(request, historyMessages);
            case PLAN_EXECUTE -> planExecuteFlow(request, historyMessages);
        };

        return flow
                .doOnNext(trace::capture)
                .doOnComplete(() -> persistTurn(request, trace));
    }

    private Flux<AgentDelta> plainContent(AgentRequest request, List<Message> historyMessages) {
        if (enabledToolsByName.isEmpty()) {
            return plainDirectContent(request, historyMessages, "agent-plain-content")
                    .concatWith(Flux.just(AgentDelta.finish("stop")));
        }

        return plainSingleToolFlow(request, historyMessages)
                .onErrorResume(ex -> Flux.concat(
                        Flux.defer(() -> {
                            log.error("[agent:{}] plain single-tool flow failed, fallback to plain content", id(), ex);
                            return Flux.empty();
                        }),
                        Flux.just(AgentDelta.thinking("PLAIN 单工具流程失败，降级为直接回答。")),
                        plainDirectContent(request, historyMessages, "agent-plain-content-fallback"),
                        Flux.just(AgentDelta.finish("stop"))
                ));
    }

    private Flux<AgentDelta> plainDirectContent(AgentRequest request, List<Message> historyMessages, String stage) {
        Flux<String> contentTextFlux = llmService.streamContentRawSse(
                        providerKey(),
                        model(),
                        systemPrompt(),
                        historyMessages,
                        request.message(),
                        stage
                )
                .onErrorResume(ex -> {
                    log.warn("[agent:{}] raw SSE stream failed for plain content, fallback to ChatClient", id(), ex);
                    return llmService.streamContent(
                            providerKey(), model(), systemPrompt(),
                            historyMessages, request.message(), stage
                    );
                })
                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"));
        return contentTextFlux.map(AgentDelta::content);
    }

    private Flux<AgentDelta> plainSingleToolFlow(AgentRequest request, List<Message> historyMessages) {
        String decisionPrompt = promptBuilder.buildPlainDecisionPrompt(request);
        log.info("[agent:{}] plain decision prompt:\n{}", id(), decisionPrompt);

        StringBuilder rawDecisionBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();
        Map<String, NativeToolCall> nativeToolCalls = new LinkedHashMap<>();
        AtomicInteger nativeToolSeq = new AtomicInteger(0);
        boolean[] formatDecided = {false};
        boolean[] plainTextDetected = {false};
        List<String> earlyBuffer = new ArrayList<>();

        Flux<AgentDelta> decisionThinkingFlux = llmService.streamDeltas(
                        providerKey(),
                        model(),
                        promptBuilder.plainDecisionSystemPrompt(),
                        historyMessages,
                        decisionPrompt,
                        toolExecutor.enabledFunctionTools(),
                        "agent-plain-decision"
                )
                .handle((chunk, sink) -> decisionChunkHandler.handleDecisionChunk(
                        chunk, sink, rawDecisionBuffer, emittedThinking,
                        nativeToolCalls, nativeToolSeq,
                        formatDecided, plainTextDetected, earlyBuffer
                ));

        return Flux.concat(
                decisionThinkingFlux,
                Flux.defer(() -> {
                    String raw = rawDecisionBuffer.toString();
                    List<PlannedToolCall> nativeCalls = decisionChunkHandler.toPlannedToolCalls(nativeToolCalls);
                    PlainDecision decision = nativeCalls.isEmpty()
                            ? decisionParser.parsePlainDecision(raw)
                            : new PlainDecision("", nativeCalls.getFirst(), true);
                    log.info("[agent:{}] plain raw decision:\n{}", id(), raw);
                    log.info("[agent:{}] plain parsed decision={}", id(), promptBuilder.toJson(decision));

                    // Content already streamed during handle phase for plain-text responses
                    if (nativeCalls.isEmpty() && !raw.isBlank() && decisionParser.readJsonObject(raw) == null) {
                        return Flux.just(AgentDelta.finish("stop"));
                    }

                    Flux<AgentDelta> summaryThinkingFlux = emittedThinking.isEmpty() && !decision.thinking().isBlank()
                            ? Flux.just(AgentDelta.thinking(decision.thinking()))
                            : Flux.empty();

                    if (!decision.valid()) {
                        return Flux.concat(
                                summaryThinkingFlux,
                                plainDirectContent(request, historyMessages, "agent-plain-content-json-fallback"),
                                Flux.just(AgentDelta.finish("stop"))
                        );
                    }

                    List<Map<String, Object>> toolRecords = new ArrayList<>();
                    Flux<AgentDelta> toolFlux = Flux.empty();
                    String finalStage = "agent-plain-final-no-tool";
                    if (decision.toolCall() != null) {
                        String callId = StringUtils.hasText(decision.toolCall().callId())
                                ? decision.toolCall().callId()
                                : "call_" + sanitize(decision.toolCall().name()) + "_plain_1";
                        toolFlux = toolExecutor.executeTool(
                                decision.toolCall(),
                                callId,
                                request.runId(),
                                toolRecords,
                                !nativeCalls.isEmpty()
                        );
                        finalStage = "agent-plain-final-with-tool";
                    }

                    String stage = finalStage;
                    Flux<AgentDelta> contentFlux = Flux.defer(() -> {
                        String finalPrompt = promptBuilder.buildPlainFinalPrompt(request, toolRecords);
                        log.info("[agent:{}] plain final prompt:\n{}", id(), finalPrompt);
                        return llmService.streamContent(
                                        providerKey(),
                                        model(),
                                        systemPrompt(),
                                        historyMessages,
                                        finalPrompt,
                                        stage
                                )
                                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                                .map(AgentDelta::content);
                    });

                    return Flux.concat(summaryThinkingFlux, toolFlux, contentFlux, Flux.just(AgentDelta.finish("stop")));
                })
        );
    }

    private Flux<AgentDelta> planExecuteFlow(AgentRequest request, List<Message> historyMessages) {
        return Flux.concat(
                        Flux.just(AgentDelta.thinking("进入 PLAN-EXECUTE 模式，正在逐步决策...\n")),
                        Flux.defer(() -> planExecuteLoop(request, historyMessages, new ArrayList<>(), 1))
                )
                .onErrorResume(ex -> Flux.concat(
                        Flux.defer(() -> {
                            log.error("[agent:{}] plan-execute flow failed, fallback to plain content", id(), ex);
                            return Flux.empty();
                        }),
                        Flux.just(AgentDelta.thinking("计划执行流程失败，降级为直接回答。")),
                        llmService.streamContent(
                                        providerKey(),
                                        model(),
                                        systemPrompt(),
                                        historyMessages,
                                        request.message(),
                                        "agent-plan-execute-fallback"
                                )
                                .switchIfEmpty(Flux.just("未获取到模型输出，请稍后重试。"))
                                .onErrorResume(inner -> Flux.just("模型调用失败，请稍后重试。"))
                                .map(AgentDelta::content),
                        Flux.just(AgentDelta.finish("stop"))
                ));
    }

    private Flux<AgentDelta> planExecuteLoop(
            AgentRequest request,
            List<Message> historyMessages,
            List<Map<String, Object>> toolRecords,
            int step
    ) {
        if (step > MAX_REACT_STEPS) {
            return finalizePlanExecuteAnswer(request, historyMessages, toolRecords, "达到 PLAN-EXECUTE 最大轮次，转为总结输出。", "agent-plan-execute-final-max");
        }

        String loopPrompt = promptBuilder.buildPlanExecuteLoopPrompt(request, toolRecords, step);
        log.info("[agent:{}] plan-execute step={} prompt:\n{}", id(), step, loopPrompt);

        String stage = "agent-plan-execute-step-" + step;
        StringBuilder rawBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();
        Map<String, NativeToolCall> nativeToolCalls = new LinkedHashMap<>();
        AtomicInteger nativeToolSeq = new AtomicInteger(0);
        boolean[] formatDecided = {false};
        boolean[] plainTextDetected = {false};
        List<String> earlyBuffer = new ArrayList<>();

        Flux<AgentDelta> stepThinkingFlux = llmService.streamDeltas(
                        providerKey(),
                        model(),
                        promptBuilder.planExecuteLoopSystemPrompt(),
                        historyMessages,
                        loopPrompt,
                        toolExecutor.enabledFunctionTools(),
                        stage,
                        true
                )
                .handle((chunk, sink) -> decisionChunkHandler.handleDecisionChunk(
                        chunk, sink, rawBuffer, emittedThinking,
                        nativeToolCalls, nativeToolSeq,
                        formatDecided, plainTextDetected, earlyBuffer
                ));

        return Flux.concat(
                stepThinkingFlux,
                Flux.defer(() -> {
                    String raw = rawBuffer.toString();
                    List<PlannedToolCall> nativeCalls = decisionChunkHandler.toPlannedToolCalls(nativeToolCalls);
                    log.info("[agent:{}] plan-execute step={} raw decision:\n{}", id(), step, raw);
                    log.info("[agent:{}] plan-execute step={} native tool_calls count={}", id(), step, nativeCalls.size());

                    // Content already streamed during handle phase for plain-text responses
                    if (nativeCalls.isEmpty() && !raw.isBlank() && decisionParser.readJsonObject(raw) == null) {
                        return Flux.just(AgentDelta.finish("stop"));
                    }

                    // If no tool_calls → done, generate final answer
                    if (nativeCalls.isEmpty()) {
                        return finalizePlanExecuteAnswer(
                                request,
                                historyMessages,
                                toolRecords,
                                "决策完成，正在流式生成最终回答。",
                                "agent-plan-execute-final-step-" + step
                        );
                    }

                    // Execute all tool_calls from this round sequentially
                    AtomicInteger toolCounter = new AtomicInteger(0);
                    Flux<AgentDelta> toolFlux = Flux.fromIterable(nativeCalls)
                            .concatMap(toolCall -> {
                                int toolSeq = toolCounter.incrementAndGet();
                                if (toolSeq > MAX_TOOL_CALLS) {
                                    return Flux.just(AgentDelta.thinking("工具调用数量超过上限，已跳过。"));
                                }
                                String callId = StringUtils.hasText(toolCall.callId())
                                        ? toolCall.callId()
                                        : "call_" + sanitize(toolCall.name()) + "_step" + step + "_" + toolSeq;

                                // Expand bash commands and execute
                                PlannedStep wrappedStep = new PlannedStep("执行工具 " + toolCall.name(), toolCall);
                                List<PlannedToolCall> expandedToolCalls = toolExecutor.expandToolCallsForStep(wrappedStep);
                                return Flux.fromIterable(expandedToolCalls)
                                        .concatMap(expanded -> {
                                            String expandedCallId = expanded == toolCall ? callId
                                                    : callId + "_" + sanitize(String.valueOf(expanded.arguments().getOrDefault("command", "")));
                                            return toolExecutor.executeTool(
                                                    expanded,
                                                    expandedCallId,
                                                    request.runId(),
                                                    toolRecords,
                                                    !nativeCalls.isEmpty()
                                            );
                                        }, 1);
                            }, 1);

                    // After executing tools, recurse to next round
                    return Flux.concat(
                            toolFlux,
                            Flux.defer(() -> planExecuteLoop(request, historyMessages, toolRecords, step + 1))
                    );
                })
        );
    }

    private Flux<AgentDelta> finalizePlanExecuteAnswer(
            AgentRequest request,
            List<Message> historyMessages,
            List<Map<String, Object>> toolRecords,
            String thinkingNote,
            String stage
    ) {
        String prompt = promptBuilder.buildPlanExecuteLoopFinalPrompt(request, toolRecords);
        log.info("[agent:{}] plan-execute final prompt:\n{}", id(), prompt);
        Flux<AgentDelta> noteFlux = thinkingNote == null || thinkingNote.isBlank()
                ? Flux.empty()
                : Flux.just(AgentDelta.thinking(thinkingNote));
        Optional<String> forcedViewport = buildForcedViewportContent(toolRecords);
        Flux<AgentDelta> contentFlux = forcedViewport
                .<Flux<AgentDelta>>map(content -> {
                    log.info("[agent:{}] force viewport output by tool mapping", id());
                    return Flux.just(AgentDelta.content(content));
                })
                .orElseGet(() -> llmService.streamContentRawSse(
                                providerKey(),
                                model(),
                                systemPrompt(),
                                historyMessages,
                                prompt,
                                stage
                        )
                        .onErrorResume(ex -> {
                            log.warn("[agent:{}] raw SSE final answer failed, fallback to ChatClient stream", id(), ex);
                            return llmService.streamContent(
                                    providerKey(),
                                    model(),
                                    systemPrompt(),
                                    historyMessages,
                                    prompt,
                                    stage
                            );
                        })
                        .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                        .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                        .map(AgentDelta::content));

        return Flux.concat(noteFlux, contentFlux, Flux.just(AgentDelta.finish("stop")));
    }

    private Flux<AgentDelta> reactFlow(AgentRequest request, List<Message> historyMessages) {
        return Flux.concat(
                        Flux.just(AgentDelta.thinking("进入 RE-ACT 模式，正在逐步决策...\n")),
                        Flux.defer(() -> reactLoop(request, historyMessages, new ArrayList<>(), 1))
                )
                .onErrorResume(ex -> Flux.concat(
                        Flux.defer(() -> {
                            log.error("[agent:{}] react flow failed, fallback to plain content", id(), ex);
                            return Flux.empty();
                        }),
                        Flux.just(AgentDelta.thinking("RE-ACT 流程失败，降级为直接回答。")),
                        llmService.streamContent(
                                        providerKey(),
                                        model(),
                                        systemPrompt(),
                                        historyMessages,
                                        request.message(),
                                        "agent-react-fallback"
                                )
                                .switchIfEmpty(Flux.just("未获取到模型输出，请稍后重试。"))
                                .onErrorResume(inner -> Flux.just("模型调用失败，请稍后重试。"))
                                .map(AgentDelta::content),
                        Flux.just(AgentDelta.finish("stop"))
                ));
    }

    private Flux<AgentDelta> reactLoop(
            AgentRequest request,
            List<Message> historyMessages,
            List<Map<String, Object>> toolRecords,
            int step
    ) {
        if (step > MAX_REACT_STEPS) {
            return finalizeReactAnswer(request, historyMessages, toolRecords, "达到 RE-ACT 最大轮次，转为总结输出。", "agent-react-final-max");
        }

        String reactPrompt = promptBuilder.buildReactPrompt(request, toolRecords, step);
        log.info("[agent:{}] react step={} prompt:\n{}", id(), step, reactPrompt);

        String stage = "agent-react-step-" + step;
        StringBuilder rawDecisionBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();
        Map<String, NativeToolCall> nativeToolCalls = new LinkedHashMap<>();
        AtomicInteger nativeToolSeq = new AtomicInteger(0);
        boolean[] formatDecided = {false};
        boolean[] plainTextDetected = {false};
        List<String> earlyBuffer = new ArrayList<>();

        Flux<AgentDelta> stepThinkingFlux = llmService.streamDeltas(
                        providerKey(),
                        model(),
                        promptBuilder.reactSystemPrompt(),
                        historyMessages,
                        reactPrompt,
                        toolExecutor.enabledFunctionTools(),
                        stage
                )
                .handle((chunk, sink) -> decisionChunkHandler.handleDecisionChunk(
                        chunk, sink, rawDecisionBuffer, emittedThinking,
                        nativeToolCalls, nativeToolSeq,
                        formatDecided, plainTextDetected, earlyBuffer
                ));

        return Flux.concat(
                stepThinkingFlux,
                Flux.defer(() -> {
                    String raw = rawDecisionBuffer.toString();
                    List<PlannedToolCall> nativeCalls = decisionChunkHandler.toPlannedToolCalls(nativeToolCalls);
                    ReactDecision decision = nativeCalls.isEmpty()
                            ? decisionParser.parseReactDecision(raw)
                            : new ReactDecision("", nativeCalls.getFirst(), false);
                    log.info("[agent:{}] react step={} raw decision:\n{}", id(), step, raw);
                    log.info("[agent:{}] react step={} parsed decision={}", id(), step, promptBuilder.toJson(decision));

                    // Content already streamed during handle phase for plain-text responses
                    if (nativeCalls.isEmpty() && !raw.isBlank() && decisionParser.readJsonObject(raw) == null) {
                        return Flux.just(AgentDelta.finish("stop"));
                    }

                    Flux<AgentDelta> summaryThinkingFlux = emittedThinking.isEmpty() && !decision.thinking().isBlank()
                            ? Flux.just(AgentDelta.thinking(decision.thinking()))
                            : Flux.empty();

                    if (decision.done()) {
                        return Flux.concat(
                                summaryThinkingFlux,
                                finalizeReactAnswer(
                                        request,
                                        historyMessages,
                                        toolRecords,
                                        "决策完成，正在流式生成最终回答。",
                                        "agent-react-final-step-" + step
                                )
                        );
                    }

                    if (decision.action() == null) {
                        return Flux.concat(
                                summaryThinkingFlux,
                                finalizeReactAnswer(
                                        request,
                                        historyMessages,
                                        toolRecords,
                                        "未获得可执行 action，转为总结输出。",
                                        "agent-react-final-empty"
                                )
                        );
                    }

                    String callId = StringUtils.hasText(decision.action().callId())
                            ? decision.action().callId()
                            : "call_" + sanitize(decision.action().name()) + "_step_" + step;

                    return Flux.concat(
                            summaryThinkingFlux,
                            toolExecutor.executeTool(
                                    decision.action(),
                                    callId,
                                    request.runId(),
                                    toolRecords,
                                    !nativeCalls.isEmpty()
                            ),
                            Flux.defer(() -> reactLoop(request, historyMessages, toolRecords, step + 1))
                    );
                })
        );
    }

    private Flux<AgentDelta> finalizeReactAnswer(
            AgentRequest request,
            List<Message> historyMessages,
            List<Map<String, Object>> toolRecords,
            String thinkingNote,
            String stage
    ) {
        String prompt = promptBuilder.buildReactFinalPrompt(request, toolRecords);
        Flux<AgentDelta> noteFlux = thinkingNote == null || thinkingNote.isBlank()
                ? Flux.empty()
                : Flux.just(AgentDelta.thinking(thinkingNote));
        Flux<AgentDelta> contentFlux = llmService.streamContentRawSse(
                        providerKey(),
                        model(),
                        systemPrompt(),
                        historyMessages,
                        prompt,
                        stage
                )
                .onErrorResume(ex -> {
                    log.warn("[agent:{}] raw SSE final answer failed, fallback to ChatClient stream", id(), ex);
                    return llmService.streamContent(
                            providerKey(),
                            model(),
                            systemPrompt(),
                            historyMessages,
                            prompt,
                            stage
                    );
                })
                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                .map(AgentDelta::content);

        return Flux.concat(noteFlux, contentFlux, Flux.just(AgentDelta.finish("stop")));
    }

    private Map<String, BaseTool> resolveEnabledTools(List<String> configuredTools) {
        Map<String, BaseTool> allToolsByName = new LinkedHashMap<>();
        for (BaseTool tool : toolRegistry.list()) {
            allToolsByName.put(normalizeToolName(tool.name()), tool);
        }

        List<String> requested = configuredTools == null ? List.of() : configuredTools;
        Map<String, BaseTool> enabled = new LinkedHashMap<>();
        for (String rawName : requested) {
            String name = normalizeToolName(rawName);
            if (name.isBlank()) {
                continue;
            }
            BaseTool tool = allToolsByName.get(name);
            if (tool == null) {
                log.warn("[agent:{}] configured tool not found and will be ignored: {}", id(), name);
                continue;
            }
            enabled.put(name, tool);
        }
        return Map.copyOf(enabled);
    }

    private String normalizeToolName(String raw) {
        return normalize(raw, "").trim().toLowerCase(Locale.ROOT);
    }

    private String sanitize(String input) {
        return normalize(input, "tool").replaceAll("[^a-zA-Z0-9_]", "_").toLowerCase(Locale.ROOT);
    }

    private List<Message> loadHistoryMessages(String chatId) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(chatId)) {
            return List.of();
        }
        try {
            return chatWindowMemoryStore.loadHistoryMessages(chatId);
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to load chat history chatId={}", id(), chatId, ex);
            return List.of();
        }
    }

    private void persistTurn(AgentRequest request, TurnTrace trace) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(request.chatId())) {
            return;
        }
        try {
            List<ChatWindowMemoryStore.RunMessage> runMessages = new ArrayList<>();
            if (StringUtils.hasText(request.message())) {
                runMessages.add(ChatWindowMemoryStore.RunMessage.user(request.message(), System.currentTimeMillis()));
            }
            runMessages.addAll(trace.runMessages());
            if (runMessages.isEmpty()) {
                return;
            }
            String runId = resolveRunId(request);
            chatWindowMemoryStore.appendRun(
                    request.chatId(),
                    runId,
                    request.query(),
                    buildSystemSnapshot(request),
                    runMessages
            );
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to persist chat turn chatId={}", id(), request.chatId(), ex);
        }
    }

    private String resolveRunId(AgentRequest request) {
        if (StringUtils.hasText(request.runId())) {
            return request.runId().trim();
        }
        return UUID.randomUUID().toString();
    }

    private ChatWindowMemoryStore.SystemSnapshot buildSystemSnapshot(AgentRequest request) {
        ChatWindowMemoryStore.SystemSnapshot snapshot = new ChatWindowMemoryStore.SystemSnapshot();
        snapshot.model = model();

        if (StringUtils.hasText(systemPrompt())) {
            ChatWindowMemoryStore.SystemMessageSnapshot systemMessage = new ChatWindowMemoryStore.SystemMessageSnapshot();
            systemMessage.role = "system";
            systemMessage.content = systemPrompt();
            snapshot.messages = List.of(systemMessage);
        }

        if (!enabledToolsByName.isEmpty()) {
            List<ChatWindowMemoryStore.SystemToolSnapshot> tools = enabledToolsByName.values().stream()
                    .sorted(java.util.Comparator.comparing(BaseTool::name))
                    .map(tool -> {
                        ChatWindowMemoryStore.SystemToolSnapshot item = new ChatWindowMemoryStore.SystemToolSnapshot();
                        item.type = toolRegistry.toolCallType(tool.name());
                        item.name = tool.name();
                        item.description = tool.description();
                        item.parameters = tool.parametersSchema();
                        return item;
                    })
                    .toList();
            if (!tools.isEmpty()) {
                snapshot.tools = tools;
            }
        }

        snapshot.stream = resolveStreamFlag(request);
        return snapshot;
    }

    private boolean resolveStreamFlag(AgentRequest request) {
        if (request == null || request.query() == null || !request.query().containsKey("stream")) {
            return true;
        }
        Object stream = request.query().get("stream");
        if (stream instanceof Boolean value) {
            return value;
        }
        if (stream instanceof Number value) {
            return value.intValue() != 0;
        }
        if (stream instanceof String value && !value.isBlank()) {
            return Boolean.parseBoolean(value.trim());
        }
        return true;
    }

    private Map<String, Object> usagePlaceholder() {
        Map<String, Object> usage = new LinkedHashMap<>();
        usage.put("input_tokens", null);
        usage.put("output_tokens", null);
        usage.put("total_tokens", null);
        return usage;
    }

    private Long durationOrNull(long startTs, long endTs) {
        if (startTs <= 0 || endTs < startTs) {
            return null;
        }
        return endTs - startTs;
    }

    private String generateToolCallId() {
        return "call_" + UUID.randomUUID().toString().replace("-", "").substring(0, 16);
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    // todo 为了显示 viewport，可能需要改造工具，增加工具的小skill，不然大模型不读 tool的description
    // todo 需要改造工具
    private Optional<String> buildForcedViewportContent(List<Map<String, Object>> toolRecords) {
        if (!shouldForceViewportOutput() || toolRecords == null || toolRecords.isEmpty()) {
            return Optional.empty();
        }

        for (int i = toolRecords.size() - 1; i >= 0; i--) {
            Map<String, Object> record = toolRecords.get(i);
            String toolName = normalizeToolName(String.valueOf(record.getOrDefault("toolName", "")));
            if (toolName.isBlank()) {
                continue;
            }
            BaseTool tool = enabledToolsByName.get(toolName);
            if (tool == null) {
                continue;
            }
            ViewportMapping mapping = parseViewportMapping(tool.description());
            if (mapping == null) {
                mapping = defaultViewportMapping(toolName);
            }
            if (mapping == null) {
                continue;
            }

            JsonNode resultNode = toObjectNode(record.get("result"));
            if (resultNode == null) {
                continue;
            }
            try {
                String json = objectMapper.writerWithDefaultPrettyPrinter().writeValueAsString(resultNode);
                return Optional.of(
                        "```viewport\n"
                                + "type=" + mapping.type() + ", key=" + mapping.key() + "\n"
                                + json + "\n"
                                + "```"
                );
            } catch (Exception ex) {
                log.warn("[agent:{}] cannot serialize viewport json for tool={}", id(), toolName, ex);
                return Optional.empty();
            }
        }

        log.warn("[agent:{}] viewport output requested but no valid tool mapping found", id());
        return Optional.empty();
    }

    private boolean shouldForceViewportOutput() {
        if ("demoviewport".equalsIgnoreCase(id())) {
            return true;
        }
        return normalize(systemPrompt(), "").contains("```viewport");
    }

    private ViewportMapping parseViewportMapping(String description) {
        if (!StringUtils.hasText(description)) {
            return null;
        }
        Matcher matcher = VIEWPORT_MAPPING_PATTERN.matcher(description);
        if (!matcher.find()) {
            return null;
        }
        String type = normalize(matcher.group(1), "").toLowerCase(Locale.ROOT);
        if (!"html".equals(type) && !"qlc".equals(type) && !"dqlc".equals(type)) {
            return null;
        }
        String key = normalize(matcher.group(2), "");
        if (key.isBlank()) {
            return null;
        }
        return new ViewportMapping(type, key);
    }

    private JsonNode toObjectNode(Object value) {
        if (value instanceof JsonNode node) {
            return node.isObject() ? node : null;
        }
        if (value == null) {
            return null;
        }
        JsonNode converted = objectMapper.valueToTree(value);
        return converted != null && converted.isObject() ? converted : null;
    }

    private ViewportMapping defaultViewportMapping(String toolName) {
        if ("mock_city_weather".equals(toolName)) {
            return new ViewportMapping("html", "show_weather_card");
        }
        if ("mock_logistics_status".equals(toolName)) {
            return new ViewportMapping("html", "show_logistics_status");
        }
        return null;
    }

    private record ViewportMapping(String type, String key) {
    }

    private final class TurnTrace {
        private final StringBuilder pendingReasoning = new StringBuilder();
        private long pendingReasoningStartedAt;
        private final StringBuilder pendingAssistant = new StringBuilder();
        private long pendingAssistantStartedAt;
        private final List<ChatWindowMemoryStore.RunMessage> orderedMessages = new ArrayList<>();
        private final Map<String, ToolTrace> toolByCallId = new LinkedHashMap<>();

        private void capture(AgentDelta delta) {
            if (delta == null) {
                return;
            }
            long now = System.currentTimeMillis();

            if (StringUtils.hasText(delta.thinking())) {
                if (pendingReasoningStartedAt <= 0) {
                    pendingReasoningStartedAt = now;
                }
                pendingReasoning.append(delta.thinking());
            }

            if (StringUtils.hasText(delta.content())) {
                if (pendingAssistantStartedAt <= 0) {
                    pendingAssistantStartedAt = now;
                }
                pendingAssistant.append(delta.content());
            }

            if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
                flushReasoning(now);
                flushAssistantContent();
                for (ToolCallDelta toolCall : delta.toolCalls()) {
                    if (toolCall == null) {
                        continue;
                    }
                    String toolCallId = StringUtils.hasText(toolCall.id()) ? toolCall.id() : generateToolCallId();
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolCallId, ToolTrace::new);
                    if (trace.firstSeenAt <= 0) {
                        trace.firstSeenAt = now;
                    }
                    if (StringUtils.hasText(toolCall.name())) {
                        trace.toolName = toolCall.name();
                    }
                    if (StringUtils.hasText(toolCall.type())) {
                        trace.toolType = toolCall.type();
                    }
                    if (StringUtils.hasText(toolCall.arguments())) {
                        trace.appendArguments(toolCall.arguments());
                    }
                }
            }

            if (delta.toolResults() != null && !delta.toolResults().isEmpty()) {
                flushReasoning(now);
                flushAssistantContent();
                for (AgentDelta.ToolResult toolResult : delta.toolResults()) {
                    if (toolResult == null || !StringUtils.hasText(toolResult.toolId())) {
                        continue;
                    }
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolResult.toolId(), ToolTrace::new);
                    if (trace.firstSeenAt <= 0) {
                        trace.firstSeenAt = now;
                    }
                    trace.resultAt = now;
                    appendAssistantToolCallIfNeeded(trace, now);
                    String result = StringUtils.hasText(toolResult.result()) ? toolResult.result() : "null";
                    orderedMessages.add(ChatWindowMemoryStore.RunMessage.toolResult(
                            trace.toolName,
                            trace.toolCallId,
                            result,
                            now,
                            durationOrNull(trace.firstSeenAt, now)
                    ));
                }
            }
        }

        private List<ChatWindowMemoryStore.RunMessage> runMessages() {
            long now = System.currentTimeMillis();
            flushReasoning(now);
            flushAssistantContent();
            toolByCallId.values().forEach(toolTrace -> appendAssistantToolCallIfNeeded(
                    toolTrace,
                    toolTrace.resultAt > 0 ? toolTrace.resultAt : now
            ));
            return List.copyOf(orderedMessages);
        }

        private void appendAssistantToolCallIfNeeded(ToolTrace trace, long ts) {
            if (trace == null || trace.recorded) {
                return;
            }
            if (!StringUtils.hasText(trace.toolName)) {
                return;
            }
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantToolCall(
                    trace.toolName,
                    trace.toolCallId,
                    trace.toolType,
                    trace.arguments(),
                    ts,
                    durationOrNull(trace.firstSeenAt, trace.resultAt > 0 ? trace.resultAt : ts),
                    usagePlaceholder()
            ));
            trace.recorded = true;
        }

        private void flushReasoning(long now) {
            if (!StringUtils.hasText(pendingReasoning)) {
                return;
            }
            long startedAt = pendingReasoningStartedAt > 0 ? pendingReasoningStartedAt : now;
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantReasoning(
                    pendingReasoning.toString(),
                    startedAt,
                    durationOrNull(startedAt, now),
                    usagePlaceholder()
            ));
            pendingReasoning.setLength(0);
            pendingReasoningStartedAt = 0L;
        }

        private void flushAssistantContent() {
            if (!StringUtils.hasText(pendingAssistant)) {
                return;
            }
            long now = System.currentTimeMillis();
            long startedAt = pendingAssistantStartedAt > 0 ? pendingAssistantStartedAt : now;
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantContent(
                    pendingAssistant.toString(),
                    startedAt,
                    durationOrNull(startedAt, now),
                    usagePlaceholder()
            ));
            pendingAssistant.setLength(0);
            pendingAssistantStartedAt = 0L;
        }
    }

    private static final class ToolTrace {
        private final String toolCallId;
        private String toolName;
        private String toolType;
        private final StringBuilder arguments = new StringBuilder();
        private long firstSeenAt;
        private long resultAt;
        private boolean recorded;

        private ToolTrace(String toolCallId) {
            this.toolCallId = Objects.requireNonNull(toolCallId);
        }

        private void appendArguments(String delta) {
            if (StringUtils.hasText(delta)) {
                this.arguments.append(delta);
            }
        }

        private String arguments() {
            return arguments.isEmpty() ? null : arguments.toString();
        }
    }

}
