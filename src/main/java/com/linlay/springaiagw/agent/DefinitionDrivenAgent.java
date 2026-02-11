package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.model.SseChunk;
import com.linlay.springaiagw.service.DeltaStreamService;
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

import java.time.Duration;
import java.time.LocalDate;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public class DefinitionDrivenAgent implements Agent {

    private static final Logger log = LoggerFactory.getLogger(DefinitionDrivenAgent.class);
    private static final int MAX_TOOL_CALLS = 6;
    private static final int MAX_REACT_STEPS = 6;
    private static final int TOOL_ARGS_CHUNK_SIZE = 8;
    private static final Duration TOOL_ARGS_DELTA_INTERVAL = Duration.ofMillis(25);
    private static final Duration TOOL_RESULT_DELTA_GAP = Duration.ofMillis(30);
    private static final Duration TOOL_EVENT_DELTA_INTERVAL = Duration.ofMillis(10);
    private static final Pattern ARG_TEMPLATE_PATTERN = Pattern.compile("^\\{\\{\\s*([a-zA-Z0-9_]+)\\.([a-zA-Z0-9_]+)([+-]\\d+d)?\\s*}}$");
    private static final Set<String> DATE_KEYWORDS_TODAY = Set.of("today", "今天");
    private static final Set<String> DATE_KEYWORDS_TOMORROW = Set.of("tomorrow", "明天");
    private static final Set<String> DATE_KEYWORDS_YESTERDAY = Set.of("yesterday", "昨天");
    private static final Set<String> DATE_KEYWORDS_DAY_AFTER_TOMORROW = Set.of("day_after_tomorrow", "day after tomorrow", "后天");
    private static final Set<String> DATE_KEYWORDS_DAY_BEFORE_YESTERDAY = Set.of("day_before_yesterday", "day before yesterday", "前天");
    private static final TypeReference<Map<String, Object>> MAP_TYPE = new TypeReference<>() {
    };

    private final AgentDefinition definition;
    private final LlmService llmService;
    @SuppressWarnings("unused")
    private final DeltaStreamService deltaStreamService;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryStore chatWindowMemoryStore;
    private final Map<String, BaseTool> enabledToolsByName;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper
    ) {
        this(definition, llmService, deltaStreamService, toolRegistry, objectMapper, null);
    }

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore
    ) {
        this.definition = definition;
        this.llmService = llmService;
        this.deltaStreamService = deltaStreamService;
        this.toolRegistry = toolRegistry;
        this.objectMapper = objectMapper;
        this.chatWindowMemoryStore = chatWindowMemoryStore;
        this.enabledToolsByName = resolveEnabledTools(definition.tools());
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
    public ProviderType providerType() {
        return definition.providerType();
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
                providerType(),
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
        Flux<String> contentTextFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        systemPrompt(),
                        historyMessages,
                        request.message(),
                        stage
                )
                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"));
        return contentTextFlux.map(AgentDelta::content);
    }

    private Flux<AgentDelta> plainSingleToolFlow(AgentRequest request, List<Message> historyMessages) {
        String decisionPrompt = buildPlainDecisionPrompt(request);
        log.info("[agent:{}] plain decision prompt:\n{}", id(), decisionPrompt);

        StringBuilder rawDecisionBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();

        Flux<AgentDelta> decisionThinkingFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        plainDecisionSystemPrompt(),
                        historyMessages,
                        decisionPrompt,
                        "agent-plain-decision"
                )
                .handle((chunk, sink) -> {
                    if (chunk == null || chunk.isEmpty()) {
                        return;
                    }
                    rawDecisionBuffer.append(chunk);
                    String delta = extractNewThinkingDelta(rawDecisionBuffer, emittedThinking);
                    if (!delta.isEmpty()) {
                        sink.next(AgentDelta.thinking(delta));
                    }
                });

        return Flux.concat(
                decisionThinkingFlux,
                Flux.defer(() -> {
                    String raw = rawDecisionBuffer.toString();
                    PlainDecision decision = parsePlainDecision(raw);
                    log.info("[agent:{}] plain raw decision:\n{}", id(), raw);
                    log.info("[agent:{}] plain parsed decision={}", id(), toJson(decision));

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
                        String callId = "call_" + sanitize(decision.toolCall().name()) + "_plain_1";
                        toolFlux = executeTool(decision.toolCall(), callId, toolRecords);
                        finalStage = "agent-plain-final-with-tool";
                    }

                    String stage = finalStage;
                    Flux<AgentDelta> contentFlux = Flux.defer(() -> {
                        String finalPrompt = buildPlainFinalPrompt(request, toolRecords);
                        log.info("[agent:{}] plain final prompt:\n{}", id(), finalPrompt);
                        return llmService.streamContent(
                                        providerType(),
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
        String plannerPrompt = buildPlannerPrompt(request);
        log.info("[agent:{}] plan-execute planner prompt:\n{}", id(), plannerPrompt);
        StringBuilder rawPlanBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();

        Flux<AgentDelta> plannerThinkingFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        plannerSystemPrompt(),
                        historyMessages,
                        plannerPrompt,
                        "agent-plan-execute-planner"
                )
                .handle((chunk, sink) -> {
                    if (chunk == null || chunk.isEmpty()) {
                        return;
                    }
                    rawPlanBuffer.append(chunk);
                    String delta = extractNewThinkingDelta(rawPlanBuffer, emittedThinking);
                    if (!delta.isEmpty()) {
                        sink.next(AgentDelta.thinking(delta));
                    }
                });

        return Flux.concat(
                        Flux.just(AgentDelta.thinking("正在生成执行计划...\n")),
                        plannerThinkingFlux,
                        Flux.defer(() -> {
                            PlannerDecision decision = parsePlannerDecision(rawPlanBuffer.toString());
                            log.info("[agent:{}] plan-execute planner raw response:\n{}", id(), rawPlanBuffer);
                            log.info("[agent:{}] plan-execute planner decision: {}", id(), toJson(decision));

                            Flux<AgentDelta> summaryThinkingFlux = emittedThinking.isEmpty()
                                    ? Flux.just(AgentDelta.thinking(buildThinkingText(decision)))
                                    : Flux.empty();

                            List<Map<String, Object>> toolRecords = new ArrayList<>();
                            Flux<AgentDelta> toolFlux = executePlanSteps(decision, toolRecords);
                            Flux<AgentDelta> contentFlux = Flux.defer(() -> {
                                log.debug("[agent:{}] plan-execute tool execution records: {}", id(), toJson(toolRecords));
                                String finalPrompt = buildPlanExecuteFinalPrompt(request, decision, toolRecords);
                                log.info("[agent:{}] plan-execute final prompt:\n{}", id(), finalPrompt);
                                return llmService.streamContent(
                                                providerType(),
                                                model(),
                                                systemPrompt(),
                                                historyMessages,
                                                finalPrompt,
                                                "agent-plan-execute-final"
                                        )
                                        .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                                        .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                                        .map(AgentDelta::content);
                            });

                            return Flux.concat(summaryThinkingFlux, toolFlux, contentFlux, Flux.just(AgentDelta.finish("stop")));
                        })
                )
                .onErrorResume(ex -> Flux.concat(
                        Flux.defer(() -> {
                            log.error("[agent:{}] plan-execute flow failed, fallback to plain content", id(), ex);
                            return Flux.empty();
                        }),
                        Flux.just(AgentDelta.thinking("计划执行流程失败，降级为直接回答。")),
                        llmService.streamContent(
                                        providerType(),
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
                                        providerType(),
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

        String reactPrompt = buildReactPrompt(request, toolRecords, step);
        log.info("[agent:{}] react step={} prompt:\n{}", id(), step, reactPrompt);

        String stage = "agent-react-step-" + step;
        StringBuilder rawDecisionBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();

        Flux<AgentDelta> stepThinkingFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        reactSystemPrompt(),
                        historyMessages,
                        reactPrompt,
                        stage
                )
                .handle((chunk, sink) -> {
                    if (chunk == null || chunk.isEmpty()) {
                        return;
                    }
                    rawDecisionBuffer.append(chunk);
                    String delta = extractNewThinkingDelta(rawDecisionBuffer, emittedThinking);
                    if (!delta.isEmpty()) {
                        sink.next(AgentDelta.thinking(delta));
                    }
                });

        return Flux.concat(
                stepThinkingFlux,
                Flux.defer(() -> {
                    String raw = rawDecisionBuffer.toString();
                    ReactDecision decision = parseReactDecision(raw);
                    log.info("[agent:{}] react step={} raw decision:\n{}", id(), step, raw);
                    log.info("[agent:{}] react step={} parsed decision={}", id(), step, toJson(decision));

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

                    String callId = "call_" + sanitize(decision.action().name()) + "_step_" + step;

                    return Flux.concat(
                            summaryThinkingFlux,
                            executeTool(decision.action(), callId, toolRecords),
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
        String prompt = buildReactFinalPrompt(request, toolRecords);
        Flux<AgentDelta> noteFlux = thinkingNote == null || thinkingNote.isBlank()
                ? Flux.empty()
                : Flux.just(AgentDelta.thinking(thinkingNote));
        Flux<AgentDelta> contentFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        systemPrompt(),
                        historyMessages,
                        prompt,
                        stage
                )
                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                .map(AgentDelta::content);

        return Flux.concat(noteFlux, contentFlux, Flux.just(AgentDelta.finish("stop")));
    }

    private String extractNewThinkingDelta(StringBuilder rawPlanBuffer, StringBuilder emittedThinking) {
        String current = extractThinkingFieldValue(rawPlanBuffer.toString());
        if (current.isEmpty() || current.length() <= emittedThinking.length()) {
            return "";
        }
        String delta = current.substring(emittedThinking.length());
        emittedThinking.append(delta);
        return delta;
    }

    private String extractThinkingFieldValue(String rawPlan) {
        if (rawPlan == null || rawPlan.isBlank()) {
            return "";
        }

        int keyStart = rawPlan.indexOf("\"thinking\"");
        if (keyStart < 0) {
            return "";
        }

        int colon = rawPlan.indexOf(':', keyStart + 10);
        if (colon < 0) {
            return "";
        }

        int valueStart = skipWhitespace(rawPlan, colon + 1);
        if (valueStart >= rawPlan.length() || rawPlan.charAt(valueStart) != '"') {
            return "";
        }

        StringBuilder value = new StringBuilder();
        int i = valueStart + 1;
        while (i < rawPlan.length()) {
            char ch = rawPlan.charAt(i);
            if (ch == '"') {
                return value.toString();
            }
            if (ch != '\\') {
                value.append(ch);
                i++;
                continue;
            }
            if (i + 1 >= rawPlan.length()) {
                return value.toString();
            }

            char escaped = rawPlan.charAt(i + 1);
            switch (escaped) {
                case '"', '\\', '/' -> value.append(escaped);
                case 'b' -> value.append('\b');
                case 'f' -> value.append('\f');
                case 'n' -> value.append('\n');
                case 'r' -> value.append('\r');
                case 't' -> value.append('\t');
                case 'u' -> {
                    if (i + 5 >= rawPlan.length()) {
                        return value.toString();
                    }
                    String hex = rawPlan.substring(i + 2, i + 6);
                    if (!isHex(hex)) {
                        value.append("\\u").append(hex);
                    } else {
                        value.append((char) Integer.parseInt(hex, 16));
                    }
                    i += 4;
                }
                default -> value.append(escaped);
            }
            i += 2;
        }

        return value.toString();
    }

    private int skipWhitespace(String text, int start) {
        int index = start;
        while (index < text.length() && Character.isWhitespace(text.charAt(index))) {
            index++;
        }
        return index;
    }

    private boolean isHex(String value) {
        for (int i = 0; i < value.length(); i++) {
            char ch = value.charAt(i);
            boolean digit = ch >= '0' && ch <= '9';
            boolean lower = ch >= 'a' && ch <= 'f';
            boolean upper = ch >= 'A' && ch <= 'F';
            if (!digit && !lower && !upper) {
                return false;
            }
        }
        return true;
    }

    private PlannerDecision parsePlannerDecision(String rawPlan) {
        JsonNode root = readJsonObject(rawPlan);
        if (root == null || !root.isObject()) {
            return fallbackPlannerDecision(rawPlan);
        }

        String thinking = normalize(root.path("thinking").asText(), "正在分解问题并判断是否需要工具调用。");
        JsonNode planNode = root.path("plan");
        boolean hasEmbeddedToolCall = planContainsEmbeddedToolCall(planNode);
        List<PlannedToolCall> legacyToolCalls = hasEmbeddedToolCall
                ? List.of()
                : readLegacyToolCalls(root.path("toolCalls"));
        List<PlannedStep> plannedSteps = readPlannedSteps(planNode, legacyToolCalls);

        if (plannedSteps.isEmpty() && !legacyToolCalls.isEmpty()) {
            for (int i = 0; i < legacyToolCalls.size(); i++) {
                PlannedToolCall toolCall = legacyToolCalls.get(i);
                plannedSteps.add(new PlannedStep(defaultPlanStepText(i + 1, toolCall), toolCall));
            }
        }

        return new PlannerDecision(thinking, plannedSteps);
    }

    private boolean planContainsEmbeddedToolCall(JsonNode planNode) {
        if (!planNode.isArray()) {
            return false;
        }
        for (JsonNode stepNode : planNode) {
            if (stepNode != null && stepNode.isObject() && stepNode.path("toolCall").isObject()) {
                return true;
            }
        }
        return false;
    }

    private List<PlannedToolCall> readLegacyToolCalls(JsonNode toolCallsNode) {
        if (!toolCallsNode.isArray()) {
            return List.of();
        }
        List<PlannedToolCall> toolCalls = new ArrayList<>();
        for (JsonNode callNode : toolCallsNode) {
            PlannedToolCall call = readPlannedToolCall(callNode);
            if (call != null) {
                toolCalls.add(call);
            }
        }
        return toolCalls;
    }

    private List<PlannedStep> readPlannedSteps(JsonNode planNode, List<PlannedToolCall> legacyToolCalls) {
        if (!planNode.isArray()) {
            return List.of();
        }

        List<PlannedStep> plannedSteps = new ArrayList<>();
        int legacyToolIndex = 0;

        for (JsonNode stepNode : planNode) {
            PlannedToolCall fallbackTool = legacyToolIndex < legacyToolCalls.size()
                    ? legacyToolCalls.get(legacyToolIndex)
                    : null;
            PlannedStep step = readPlannedStep(stepNode, fallbackTool, plannedSteps.size() + 1);
            if (step == null) {
                continue;
            }
            plannedSteps.add(step);
            if (step.toolCall() != null && fallbackTool != null && step.toolCall() == fallbackTool) {
                legacyToolIndex++;
            }
        }

        while (legacyToolIndex < legacyToolCalls.size()) {
            PlannedToolCall toolCall = legacyToolCalls.get(legacyToolIndex++);
            plannedSteps.add(new PlannedStep(defaultPlanStepText(plannedSteps.size() + 1, toolCall), toolCall));
        }

        return plannedSteps;
    }

    private PlannedStep readPlannedStep(JsonNode stepNode, PlannedToolCall fallbackToolCall, int stepIndex) {
        if (stepNode == null || stepNode.isNull()) {
            return null;
        }

        if (stepNode.isTextual()) {
            String stepText = normalize(stepNode.asText(), "");
            if (stepText.isBlank() && fallbackToolCall == null) {
                return null;
            }
            String normalizedStepText = stepText.isBlank()
                    ? defaultPlanStepText(stepIndex, fallbackToolCall)
                    : stepText;
            return new PlannedStep(normalizedStepText, fallbackToolCall);
        }

        if (!stepNode.isObject()) {
            String stepText = normalize(stepNode.asText(), "");
            if (stepText.isBlank() && fallbackToolCall == null) {
                return null;
            }
            String normalizedStepText = stepText.isBlank()
                    ? defaultPlanStepText(stepIndex, fallbackToolCall)
                    : stepText;
            return new PlannedStep(normalizedStepText, fallbackToolCall);
        }

        String stepText = normalize(stepNode.path("step").asText(), "");
        if (stepText.isBlank()) {
            stepText = normalize(stepNode.path("description").asText(), "");
        }
        if (stepText.isBlank()) {
            stepText = normalize(stepNode.path("text").asText(), "");
        }

        PlannedToolCall stepToolCall = null;
        JsonNode stepToolCallNode = stepNode.path("toolCall");
        if (stepToolCallNode.isObject()) {
            stepToolCall = readPlannedToolCall(stepToolCallNode);
        }
        if (stepToolCall == null) {
            stepToolCall = fallbackToolCall;
        }

        if (stepText.isBlank() && stepToolCall == null) {
            return null;
        }
        String normalizedStepText = stepText.isBlank()
                ? defaultPlanStepText(stepIndex, stepToolCall)
                : stepText;
        return new PlannedStep(normalizedStepText, stepToolCall);
    }

    private String defaultPlanStepText(int stepIndex, PlannedToolCall toolCall) {
        if (toolCall != null) {
            return "执行工具 " + toolCall.name();
        }
        return "执行步骤" + stepIndex;
    }

    private PlannerDecision fallbackPlannerDecision(String rawPlan) {
        String thinking = "根据用户问题生成计划，按需调用工具，最后输出可执行结论。";
        if (rawPlan != null && !rawPlan.isBlank()) {
            thinking += " 原始规划输出无法解析，已降级为无工具执行。";
        }

        return new PlannerDecision(
                thinking,
                List.of(
                        new PlannedStep("确认用户目标与输入约束", null),
                        new PlannedStep("判断是否需要工具辅助", null),
                        new PlannedStep("输出结论与下一步建议", null)
                )
        );
    }

    private ReactDecision parseReactDecision(String rawDecision) {
        JsonNode root = readJsonObject(rawDecision);
        if (root == null || !root.isObject()) {
            return new ReactDecision(
                    "RE-ACT 输出无法解析为 JSON，转为直接生成最终回答。",
                    null,
                    true
            );
        }

        String thinking = normalize(root.path("thinking").asText(), "");

        boolean done = root.path("done").asBoolean(false);
        String finalAnswer = normalize(root.path("finalAnswer").asText(), "");
        if (!finalAnswer.isBlank() && !"null".equalsIgnoreCase(finalAnswer)) {
            // Backward compatibility: old prompt may still return finalAnswer directly.
            done = true;
        }

        PlannedToolCall action = null;
        JsonNode actionNode = root.path("action");
        if (actionNode.isObject()) {
            action = readPlannedToolCall(actionNode);
        }

        if (done) {
            action = null;
        }
        return new ReactDecision(thinking, action, done);
    }

    private PlainDecision parsePlainDecision(String rawDecision) {
        JsonNode root = readJsonObject(rawDecision);
        if (root == null || !root.isObject()) {
            return new PlainDecision(
                    "PLAIN 决策输出无法解析为 JSON，转为直接回答。",
                    null,
                    false
            );
        }

        String thinking = normalize(root.path("thinking").asText(), "");
        PlannedToolCall toolCall = null;

        JsonNode toolCallNode = root.path("toolCall");
        if (toolCallNode.isObject()) {
            toolCall = readPlannedToolCall(toolCallNode);
        }

        if (toolCall == null) {
            JsonNode actionNode = root.path("action");
            if (actionNode.isObject()) {
                toolCall = readPlannedToolCall(actionNode);
            }
        }

        if (toolCall == null) {
            JsonNode toolCallsNode = root.path("toolCalls");
            if (toolCallsNode.isArray()) {
                for (JsonNode callNode : toolCallsNode) {
                    toolCall = readPlannedToolCall(callNode);
                    if (toolCall != null) {
                        break;
                    }
                }
            }
        }

        return new PlainDecision(thinking, toolCall, true);
    }

    private PlannedToolCall readPlannedToolCall(JsonNode callNode) {
        String toolName = normalizeToolName(callNode.path("name").asText());
        if (toolName.isBlank()) {
            return null;
        }

        Map<String, Object> arguments = new LinkedHashMap<>();
        JsonNode argumentsNode = callNode.path("arguments");
        if (argumentsNode.isObject()) {
            Map<String, Object> converted = objectMapper.convertValue(argumentsNode, MAP_TYPE);
            if (converted != null) {
                arguments.putAll(converted);
            }
        }

        return new PlannedToolCall(toolName, arguments);
    }

    private Flux<AgentDelta> executePlanSteps(PlannerDecision decision, List<Map<String, Object>> records) {
        if (decision.steps().isEmpty()) {
            return Flux.empty();
        }
        AtomicInteger toolCounter = new AtomicInteger(0);
        int totalSteps = decision.steps().size();
        return Flux.range(0, totalSteps)
                .concatMap(i -> {
                    PlannedStep step = decision.steps().get(i);
                    int stepIndex = i + 1;
                    log.info("[agent:{}] plan-execute run step {}/{}: {}", id(), stepIndex, totalSteps, normalize(step.step(), "执行步骤" + stepIndex));
                    Flux<AgentDelta> stepThinkingFlux = Flux.just(AgentDelta.thinking(
                            "执行步骤 " + stepIndex + "/" + totalSteps + "："
                                    + normalize(step.step(), "执行步骤" + stepIndex)
                    ));
                    if (step.toolCall() == null) {
                        return stepThinkingFlux;
                    }
                    List<PlannedToolCall> expandedToolCalls = expandToolCallsForStep(step);
                    Flux<AgentDelta> expandedFlux = Flux.fromIterable(expandedToolCalls)
                            .concatMap(toolCall -> {
                                int toolSeq = toolCounter.incrementAndGet();
                                if (toolSeq > MAX_TOOL_CALLS) {
                                    return Flux.just(AgentDelta.thinking("工具调用数量超过上限，已跳过该步骤的工具执行。"));
                                }
                                String callId = "call_" + sanitize(toolCall.name()) + "_" + toolSeq;
                                return executeTool(toolCall, callId, records);
                            }, 1);

                    return Flux.concat(stepThinkingFlux, expandedFlux);
                }, 1);
    }

    private List<PlannedToolCall> expandToolCallsForStep(PlannedStep step) {
        if (step == null || step.toolCall() == null) {
            return List.of();
        }
        PlannedToolCall toolCall = step.toolCall();
        if (!"bash".equals(normalizeToolName(toolCall.name()))) {
            return List.of(toolCall);
        }
        if (toolCall.arguments() == null) {
            return List.of(toolCall);
        }

        Object rawCommand = toolCall.arguments().get("command");
        if (!(rawCommand instanceof String commandText)) {
            return List.of(toolCall);
        }

        List<String> splitCommands = splitBashCommands(commandText);
        if (splitCommands.size() <= 1) {
            return List.of(toolCall);
        }

        log.info("[agent:{}] split bash composite command in step '{}' to {} commands: {}", id(), normalize(step.step(), "unknown-step"), splitCommands.size(), splitCommands);

        List<PlannedToolCall> expanded = new ArrayList<>();
        for (String splitCommand : splitCommands) {
            Map<String, Object> splitArgs = new LinkedHashMap<>(toolCall.arguments());
            splitArgs.put("command", splitCommand);
            expanded.add(new PlannedToolCall(toolCall.name(), splitArgs));
        }
        return expanded;
    }

    private List<String> splitBashCommands(String commandText) {
        if (commandText == null || commandText.isBlank()) {
            return List.of();
        }
        String[] parts = commandText.split("\\s*(?:&&|\\|\\||;|\\|)\\s*");
        List<String> commands = new ArrayList<>();
        for (String part : parts) {
            String normalized = normalize(part, "").trim();
            if (!normalized.isBlank()) {
                commands.add(normalized);
            }
        }
        return commands;
    }

    private Flux<AgentDelta> executeTool(PlannedToolCall plannedCall, String callId, List<Map<String, Object>> records) {
        Map<String, Object> args = new LinkedHashMap<>();
        if (plannedCall.arguments() != null) {
            args.putAll(plannedCall.arguments());
        }
        Map<String, Object> resolvedArgs = resolveToolArguments(plannedCall.name(), args, records);

        if (!resolvedArgs.equals(args)) {
            log.info("[agent:{}] resolved tool args callId={}, tool={}, planned={}, resolved={}", id(), callId, plannedCall.name(), toJson(args), toJson(resolvedArgs));
        }

        String argumentsJson = toJson(resolvedArgs);
        Flux<AgentDelta> toolArgsFlux = toToolArgsDeltas(callId, plannedCall.name(), argumentsJson);
        Mono<AgentDelta> toolResultDelta = Mono.delay(TOOL_RESULT_DELTA_GAP).then(Mono.fromCallable(() -> {
                    JsonNode result = safeInvoke(plannedCall.name(), resolvedArgs);
                    Map<String, Object> record = toolRecord(callId, plannedCall.name(), resolvedArgs, result);
                    records.add(record);
                    log.info("[agent:{}] tool finished callId={}, tool={}, record={}", id(), callId, plannedCall.name(), toJson(record));
                    return AgentDelta.toolResult(callId, result);
                })
                .subscribeOn(Schedulers.boundedElastic()));

        return Flux.concat(toolArgsFlux, toolResultDelta)
                .delayElements(TOOL_EVENT_DELTA_INTERVAL);
    }

    private Map<String, Object> resolveToolArguments(String toolName, Map<String, Object> plannedArgs, List<Map<String, Object>> records) {
        Map<String, Object> resolved = new LinkedHashMap<>();
        for (Map.Entry<String, Object> entry : plannedArgs.entrySet()) {
            Object value = resolveArgumentValue(entry.getKey(), entry.getValue(), resolved, records);
            resolved.put(entry.getKey(), value);
        }
        if (resolved.isEmpty()) {
            return resolved;
        }

        if ("mock_city_weather".equals(normalizeToolName(toolName))) {
            Object rawDate = resolved.get("date");
            if (rawDate instanceof String dateText) {
                String normalizedDate = resolveRelativeDate(dateText, String.valueOf(resolved.getOrDefault("city", "")), records);
                resolved.put("date", normalizedDate);
            }
        }
        return resolved;
    }

    private Object resolveArgumentValue(
            String key,
            Object rawValue,
            Map<String, Object> partialResolvedArgs,
            List<Map<String, Object>> records
    ) {
        if (rawValue instanceof String text) {
            String trimmed = text.trim();
            Object templateResolved = resolveTemplateValue(trimmed, records);
            if (templateResolved != null) {
                return templateResolved;
            }
            if ("date".equalsIgnoreCase(normalize(key, ""))) {
                String city = String.valueOf(partialResolvedArgs.getOrDefault("city", ""));
                return resolveRelativeDate(trimmed, city, records);
            }
            return rawValue;
        }

        if (rawValue instanceof Map<?, ?> rawMap) {
            Map<String, Object> resolvedMap = new LinkedHashMap<>();
            for (Map.Entry<?, ?> entry : rawMap.entrySet()) {
                String childKey = String.valueOf(entry.getKey());
                Object childValue = resolveArgumentValue(childKey, entry.getValue(), resolvedMap, records);
                resolvedMap.put(childKey, childValue);
            }
            return resolvedMap;
        }

        if (rawValue instanceof List<?> rawList) {
            List<Object> resolvedList = new ArrayList<>();
            for (Object item : rawList) {
                resolvedList.add(resolveArgumentValue(key, item, partialResolvedArgs, records));
            }
            return resolvedList;
        }

        return rawValue;
    }

    private Object resolveTemplateValue(String value, List<Map<String, Object>> records) {
        Matcher matcher = ARG_TEMPLATE_PATTERN.matcher(value);
        if (!matcher.matches()) {
            return null;
        }

        String toolName = normalizeToolName(matcher.group(1));
        String fieldName = matcher.group(2);
        String dayOffsetText = matcher.group(3);
        Integer dayOffset = parseDayOffset(dayOffsetText);

        JsonNode fieldNode = latestToolResultField(records, toolName, fieldName);
        if (fieldNode == null || fieldNode.isNull()) {
            return null;
        }
        if (dayOffset != null) {
            if (!fieldNode.isTextual()) {
                return null;
            }
            LocalDate parsed = parseLocalDate(fieldNode.asText());
            if (parsed == null) {
                return null;
            }
            return parsed.plusDays(dayOffset).toString();
        }
        return objectMapper.convertValue(fieldNode, Object.class);
    }

    private Integer parseDayOffset(String dayOffsetText) {
        if (dayOffsetText == null || dayOffsetText.isBlank()) {
            return null;
        }
        String normalized = dayOffsetText.trim().toLowerCase(Locale.ROOT);
        if (!normalized.endsWith("d")) {
            return null;
        }
        try {
            return Integer.parseInt(normalized.substring(0, normalized.length() - 1));
        } catch (NumberFormatException ex) {
            return null;
        }
    }

    private JsonNode latestToolResultField(List<Map<String, Object>> records, String toolName, String fieldName) {
        if (records == null || records.isEmpty()) {
            return null;
        }
        for (int i = records.size() - 1; i >= 0; i--) {
            Map<String, Object> record = records.get(i);
            String recordToolName = normalizeToolName(String.valueOf(record.getOrDefault("toolName", "")));
            if (!toolName.equals(recordToolName)) {
                continue;
            }
            Object result = record.get("result");
            if (!(result instanceof JsonNode resultNode) || !resultNode.isObject()) {
                continue;
            }
            JsonNode field = resultNode.path(fieldName);
            if (!field.isMissingNode()) {
                return field;
            }
        }
        return null;
    }

    private String resolveRelativeDate(String value, String city, List<Map<String, Object>> records) {
        Integer dayOffset = relativeDayOffset(value);
        if (dayOffset == null) {
            return value;
        }
        LocalDate baseDate = latestCityDate(records, city);
        if (baseDate == null) {
            return value;
        }
        return baseDate.plusDays(dayOffset).toString();
    }

    private Integer relativeDayOffset(String value) {
        if (value == null || value.isBlank()) {
            return null;
        }
        String trimmed = value.trim();
        String lower = trimmed.toLowerCase(Locale.ROOT);
        if (DATE_KEYWORDS_TODAY.contains(lower) || DATE_KEYWORDS_TODAY.contains(trimmed)) {
            return 0;
        }
        if (DATE_KEYWORDS_TOMORROW.contains(lower) || DATE_KEYWORDS_TOMORROW.contains(trimmed)) {
            return 1;
        }
        if (DATE_KEYWORDS_YESTERDAY.contains(lower) || DATE_KEYWORDS_YESTERDAY.contains(trimmed)) {
            return -1;
        }
        if (DATE_KEYWORDS_DAY_AFTER_TOMORROW.contains(lower) || DATE_KEYWORDS_DAY_AFTER_TOMORROW.contains(trimmed)) {
            return 2;
        }
        if (DATE_KEYWORDS_DAY_BEFORE_YESTERDAY.contains(lower) || DATE_KEYWORDS_DAY_BEFORE_YESTERDAY.contains(trimmed)) {
            return -2;
        }
        return null;
    }

    private LocalDate latestCityDate(List<Map<String, Object>> records, String city) {
        if (records == null || records.isEmpty()) {
            return null;
        }
        String normalizedTargetCity = normalizeCity(city);
        for (int i = records.size() - 1; i >= 0; i--) {
            Map<String, Object> record = records.get(i);
            String toolName = normalizeToolName(String.valueOf(record.getOrDefault("toolName", "")));
            if (!"city_datetime".equals(toolName)) {
                continue;
            }
            Object result = record.get("result");
            if (!(result instanceof JsonNode resultNode) || !resultNode.isObject()) {
                continue;
            }
            if (!normalizedTargetCity.isBlank()) {
                String recordCity = normalizeCity(resultNode.path("city").asText(""));
                if (!recordCity.isBlank() && !recordCity.equals(normalizedTargetCity)) {
                    continue;
                }
            }
            LocalDate parsed = parseLocalDate(resultNode.path("date").asText(""));
            if (parsed != null) {
                return parsed;
            }
        }
        return null;
    }

    private LocalDate parseLocalDate(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        try {
            return LocalDate.parse(text.trim());
        } catch (DateTimeParseException ex) {
            return null;
        }
    }

    private String normalizeCity(String city) {
        if (city == null) {
            return "";
        }
        String normalized = city.trim().toLowerCase(Locale.ROOT).replace(" ", "");
        Set<String> aliases = new HashSet<>(Set.of("shanghai", "shanghaishi", "上海", "上海市"));
        if (aliases.contains(normalized)) {
            return "shanghai";
        }
        return normalized;
    }

    private Map<String, Object> toolRecord(String callId, String toolName, Map<String, Object> args, JsonNode result) {
        Map<String, Object> record = new LinkedHashMap<>();
        record.put("callId", callId);
        record.put("toolName", toolName);
        record.put("arguments", args);
        record.put("result", result);
        return record;
    }

    private Flux<AgentDelta> toToolArgsDeltas(String callId, String toolName, String argumentsJson) {
        String normalized = normalize(argumentsJson, "{}");
        List<AgentDelta> chunks = new ArrayList<>();
        for (int start = 0; start < normalized.length(); start += TOOL_ARGS_CHUNK_SIZE) {
            int end = Math.min(start + TOOL_ARGS_CHUNK_SIZE, normalized.length());
            String delta = normalized.substring(start, end);
            chunks.add(AgentDelta.toolCalls(List.of(toolCall(callId, toolName, delta))));
        }

        if (chunks.isEmpty()) {
            chunks.add(AgentDelta.toolCalls(List.of(toolCall(callId, toolName, "{}"))));
        }
        if (chunks.size() == 1) {
            return Flux.just(chunks.getFirst());
        }
        return Flux.just(chunks.getFirst())
                .concatWith(Flux.fromIterable(chunks.subList(1, chunks.size())).delayElements(TOOL_ARGS_DELTA_INTERVAL));
    }

    private JsonNode safeInvoke(String toolName, Map<String, Object> args) {
        String normalizedName = normalizeToolName(toolName);
        try {
            if (!enabledToolsByName.containsKey(normalizedName)) {
                ObjectNode error = objectMapper.createObjectNode();
                error.put("tool", normalizedName);
                error.put("ok", false);
                error.put("error", "Tool is not enabled for this agent: " + normalizedName);
                return error;
            }
            return toolRegistry.invoke(normalizedName, args);
        } catch (Exception ex) {
            ObjectNode error = objectMapper.createObjectNode();
            error.put("tool", normalizedName);
            error.put("ok", false);
            error.put("error", ex.getMessage());
            return error;
        }
    }

    private String buildThinkingText(PlannerDecision decision) {
        StringBuilder builder = new StringBuilder();
        builder.append(normalize(decision.thinking(), "正在拆解问题并生成执行路径。"));

        if (!decision.steps().isEmpty()) {
            builder.append("\n计划：");
            int i = 1;
            for (PlannedStep step : decision.steps()) {
                builder.append("\n").append(i++).append(". ").append(normalize(step.step(), "执行步骤"));
                if (step.toolCall() != null) {
                    builder.append(" (tool: ").append(step.toolCall().name()).append(")");
                }
            }
        }

        return builder.toString();
    }

    private String buildPlannerPrompt(AgentRequest request) {
        String bashHint = enabledToolsByName.containsKey("bash")
                ? "4) 需要查看本地文件、目录、磁盘或系统状态时，优先使用 bash。"
                : "4) 如需工具调用，必须从工具列表中选择。";
        return """
                用户问题：%s
                可用工具：
                %s

                请只输出 JSON（不要代码块、不要额外解释），格式：
                {
                  "thinking": "你的关键思考",
                  "plan": [
                    {"step": "步骤1", "toolCall": {"name": "tool_name", "arguments": {"k": "v"}}},
                    {"step": "步骤2", "toolCall": null}
                  ]
                }

                约束：
                1) thinking 用中文，一句话。
                2) plan 输出 1-%d 条有序步骤，每一步都必须包含 step 字段。
                3) toolCall 必须绑定在 plan 的每个步骤内；每步最多一个 toolCall，没有则写 null。
                %s
                5) toolCall.name 必须来自工具列表。
                6) arguments 必须显式给出且符合工具定义，不要依赖任何隐式参数补齐。
                7) plan 会严格按顺序逐步执行，不要生成独立于步骤的工具调用列表。
                8) 当后续工具参数依赖前序工具结果时，必须使用模板参数，例如 "date":"{{city_datetime.date+1d}}"。
                9) 不要直接使用 today/tomorrow/昨天/明天 作为最终日期参数，应改为具体日期或模板表达式。
                10) bash.command 必须是单条命令，不要使用 &&、||、; 或管道拼接多条命令。
                """.formatted(
                request.message(),
                enabledToolsPrompt(),
                MAX_TOOL_CALLS,
                bashHint
        );
    }

    private String buildReactPrompt(AgentRequest request, List<Map<String, Object>> toolRecords, int step) {
        return """
                用户问题：%s
                当前轮次：%d/%d
                历史工具结果(JSON)：%s
                可用工具：
                %s

                请只输出 JSON（不要代码块、不要额外解释），格式：
                {
                  "thinking": "你的关键思考",
                  "action": {"name": "tool_name", "arguments": {"k": "v"}} 或 null,
                  "done": true 或 false
                }

                约束：
                1) thinking 用中文，一句话。
                2) 需要继续查证时：done=false，填写 action。
                3) 已可直接回答时：done=true，action 设为 null。
                4) action.name 必须来自工具列表，arguments 必须显式给出。
                5) 每轮最多调用一个工具。
                """.formatted(
                request.message(),
                step,
                MAX_REACT_STEPS,
                toJson(toolRecords),
                enabledToolsPrompt()
        );
    }

    private String buildPlainDecisionPrompt(AgentRequest request) {
        return """
                用户问题：%s
                可用工具：
                %s

                请先判断是否需要工具。只输出 JSON（不要代码块、不要额外解释），格式：
                {
                  "thinking": "你的关键思考",
                  "toolCall": {"name": "tool_name", "arguments": {"k": "v"}} 或 null
                }

                约束：
                1) thinking 用中文，一句话。
                2) toolCall 最多一个，不要输出 toolCalls 数组。
                3) 若无需工具，toolCall 必须为 null。
                4) 若需要工具，只能从工具列表中选择一个。
                5) arguments 必须显式给出且符合工具定义，不要依赖隐式参数补齐。
                """.formatted(
                request.message(),
                enabledToolsPrompt()
        );
    }

    private String plannerSystemPrompt() {
        return normalize(systemPrompt(), "你是通用助理")
                + "\n你当前处于任务编排阶段：先深度思考，再给出计划，并按需声明工具调用。";
    }

    private String reactSystemPrompt() {
        return normalize(systemPrompt(), "你是通用助理")
                + "\n你当前处于 RE-ACT 阶段：每轮只做一个动作决策（继续调用工具或直接给最终回答）。";
    }

    private String plainDecisionSystemPrompt() {
        return normalize(systemPrompt(), "你是通用助理")
                + "\n你当前处于 PLAIN 单工具决策阶段：先判断是否需要工具；若需要，只能调用一个工具。";
    }

    private String buildPlanExecuteFinalPrompt(
            AgentRequest request,
            PlannerDecision decision,
            List<Map<String, Object>> toolRecords
    ) {
        String toolResultJson = toJson(toolRecords);
        String planText = decision.steps().isEmpty()
                ? "[]"
                : decision.steps().stream()
                .map(step -> normalize(step.step(), "执行步骤"))
                .reduce((left, right) -> left + " | " + right)
                .orElse("[]");

        return """
                用户问题：%s
                思考摘要：%s
                计划步骤：%s
                工具执行结果(JSON)：%s

                请基于以上信息输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 必要时给简短行动建议。
                4) 保持简洁、可执行。
                """.formatted(
                request.message(),
                normalize(decision.thinking(), "(empty)"),
                planText,
                toolResultJson
        );
    }

    private String buildReactFinalPrompt(AgentRequest request, List<Map<String, Object>> toolRecords) {
        return """
                用户问题：%s
                工具执行结果(JSON)：%s

                请输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 必要时给简短行动建议。
                4) 保持简洁、可执行。
                """.formatted(
                request.message(),
                toJson(toolRecords)
        );
    }

    private String buildPlainFinalPrompt(AgentRequest request, List<Map<String, Object>> toolRecords) {
        return """
                用户问题：%s
                工具执行结果(JSON)：%s

                请输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 保持简洁、可执行。
                """.formatted(
                request.message(),
                toJson(toolRecords)
        );
    }

    private String enabledToolsPrompt() {
        if (enabledToolsByName.isEmpty()) {
            return "- 无可用工具";
        }
        return enabledToolsByName.values().stream()
                .sorted(Comparator.comparing(BaseTool::name))
                .map(tool -> "- " + tool.name() + "：" + tool.description())
                .reduce((left, right) -> left + "\n" + right)
                .orElse("- 无可用工具");
    }

    private List<String> readTextArray(JsonNode node) {
        if (!node.isArray()) {
            return List.of();
        }

        List<String> values = new ArrayList<>();
        for (JsonNode item : node) {
            String text = normalize(item.asText(), "");
            if (!text.isBlank()) {
                values.add(text);
            }
        }
        return values;
    }

    private JsonNode readJsonObject(String raw) {
        if (raw == null || raw.isBlank()) {
            return null;
        }

        String normalized = raw.trim();
        if (normalized.startsWith("```") && normalized.endsWith("```")) {
            normalized = normalized.substring(3, normalized.length() - 3).trim();
            if (normalized.startsWith("json")) {
                normalized = normalized.substring(4).trim();
            }
        }

        try {
            return objectMapper.readTree(normalized);
        } catch (Exception ex) {
            int start = normalized.indexOf('{');
            int end = normalized.lastIndexOf('}');
            if (start < 0 || end <= start) {
                return null;
            }
            String body = normalized.substring(start, end + 1);
            try {
                return objectMapper.readTree(body);
            } catch (Exception ignored) {
                return null;
            }
        }
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

    private SseChunk.ToolCall toolCall(String callId, String toolName, String arguments) {
        return new SseChunk.ToolCall(callId, "function", new SseChunk.Function(toolName, arguments));
    }

    private String toJson(Object value) {
        try {
            return objectMapper.writeValueAsString(value);
        } catch (JsonProcessingException ex) {
            throw new IllegalStateException("Cannot serialize json", ex);
        }
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
                runMessages.add(ChatWindowMemoryStore.RunMessage.user(request.message()));
            }
            runMessages.addAll(trace.runMessages());
            if (runMessages.isEmpty()) {
                return;
            }
            chatWindowMemoryStore.appendRun(
                    request.chatId(),
                    resolveRunId(request),
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

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    private final class TurnTrace {
        private final StringBuilder pendingAssistant = new StringBuilder();
        private final List<ChatWindowMemoryStore.RunMessage> orderedMessages = new ArrayList<>();
        private final Map<String, ToolTrace> toolByCallId = new LinkedHashMap<>();

        private void capture(AgentDelta delta) {
            if (delta == null) {
                return;
            }

            if (StringUtils.hasText(delta.content())) {
                pendingAssistant.append(delta.content());
            }

            if (delta.toolCalls() != null) {
                flushAssistantContent();
                for (SseChunk.ToolCall toolCall : delta.toolCalls()) {
                    if (toolCall == null || !StringUtils.hasText(toolCall.id())) {
                        continue;
                    }
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolCall.id(), ToolTrace::new);
                    if (toolCall.function() != null) {
                        if (StringUtils.hasText(toolCall.function().name())) {
                            trace.toolName = toolCall.function().name();
                        }
                        if (StringUtils.hasText(toolCall.function().arguments())) {
                            trace.appendArguments(toolCall.function().arguments());
                        }
                    }
                }
            }

            if (delta.toolResults() != null) {
                flushAssistantContent();
                for (AgentDelta.ToolResult toolResult : delta.toolResults()) {
                    if (toolResult == null || !StringUtils.hasText(toolResult.toolId())) {
                        continue;
                    }
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolResult.toolId(), ToolTrace::new);
                    appendAssistantToolCallIfNeeded(trace);
                    String result = StringUtils.hasText(toolResult.result()) ? toolResult.result() : "null";
                    orderedMessages.add(ChatWindowMemoryStore.RunMessage.toolResult(
                            trace.toolName,
                            trace.toolCallId,
                            trace.arguments(),
                            result
                    ));
                }
            }
        }

        private List<ChatWindowMemoryStore.RunMessage> runMessages() {
            flushAssistantContent();
            toolByCallId.values().forEach(this::appendAssistantToolCallIfNeeded);
            return List.copyOf(orderedMessages);
        }

        private void appendAssistantToolCallIfNeeded(ToolTrace trace) {
            if (trace == null || trace.recorded) {
                return;
            }
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantToolCall(
                    trace.toolName,
                    trace.toolCallId,
                    trace.arguments()
            ));
            trace.recorded = true;
        }

        private void flushAssistantContent() {
            if (!StringUtils.hasText(pendingAssistant)) {
                return;
            }
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantContent(pendingAssistant.toString()));
            pendingAssistant.setLength(0);
        }
    }

    private static final class ToolTrace {
        private final String toolCallId;
        private String toolName;
        private final StringBuilder arguments = new StringBuilder();
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

    private record PlannerDecision(
            String thinking,
            List<PlannedStep> steps
    ) {
    }

    private record PlannedStep(
            String step,
            PlannedToolCall toolCall
    ) {
    }

    private record ReactDecision(
            String thinking,
            PlannedToolCall action,
            boolean done
    ) {
    }

    private record PlainDecision(
            String thinking,
            PlannedToolCall toolCall,
            boolean valid
    ) {
    }

    private record PlannedToolCall(
            String name,
            Map<String, Object> arguments
    ) {
    }

}
