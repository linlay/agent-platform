package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.model.SseChunk;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import reactor.core.publisher.Flux;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

public class DefinitionDrivenAgent implements Agent {

    private static final Logger log = LoggerFactory.getLogger(DefinitionDrivenAgent.class);
    private static final int MAX_TOOL_CALLS = 6;
    private static final int MAX_REACT_STEPS = 6;
    private static final TypeReference<Map<String, Object>> MAP_TYPE = new TypeReference<>() {
    };

    private final AgentDefinition definition;
    private final LlmService llmService;
    @SuppressWarnings("unused")
    private final DeltaStreamService deltaStreamService;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper;
    private final Map<String, BaseTool> enabledToolsByName;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper
    ) {
        this.definition = definition;
        this.llmService = llmService;
        this.deltaStreamService = deltaStreamService;
        this.toolRegistry = toolRegistry;
        this.objectMapper = objectMapper;
        this.enabledToolsByName = resolveEnabledTools(definition.tools());
    }

    @Override
    public String id() {
        return definition.id();
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

        return switch (definition.mode()) {
            case PLAIN -> plainContent(request);
            case RE_ACT -> reactFlow(request);
            case PLAN_EXECUTE -> planExecuteFlow(request);
        };
    }

    private Flux<AgentDelta> plainContent(AgentRequest request) {
        Flux<String> contentTextFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        systemPrompt(),
                        request.message(),
                        "agent-plain-content"
                )
                .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"));
        return contentTextFlux
                .map(AgentDelta::content)
                .concatWith(Flux.just(AgentDelta.finish("stop")));
    }

    private Flux<AgentDelta> planExecuteFlow(AgentRequest request) {
        String plannerPrompt = buildPlannerPrompt(request);
        log.info("[agent:{}] plan-execute planner prompt:\n{}", id(), plannerPrompt);
        StringBuilder rawPlanBuffer = new StringBuilder();
        StringBuilder emittedThinking = new StringBuilder();

        Flux<AgentDelta> plannerThinkingFlux = llmService.streamContent(
                        providerType(),
                        model(),
                        plannerSystemPrompt(),
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

                            ToolExecution toolExecution = executePlannedTools(decision);
                            Flux<AgentDelta> toolFlux = Flux.fromIterable(toolExecution.deltas());
                            log.info("[agent:{}] plan-execute tool execution records: {}", id(), toJson(toolExecution.records()));

                            String finalPrompt = buildPlanExecuteFinalPrompt(request, decision, toolExecution.records());
                            log.info("[agent:{}] plan-execute final prompt:\n{}", id(), finalPrompt);
                            Flux<AgentDelta> contentFlux = llmService.streamContent(
                                            providerType(),
                                            model(),
                                            systemPrompt(),
                                            finalPrompt,
                                            "agent-plan-execute-final"
                                    )
                                    .switchIfEmpty(Flux.just("未获取到模型输出，请检查 provider/model/sysPrompt 配置。"))
                                    .onErrorResume(ex -> Flux.just("模型调用失败，请稍后重试。"))
                                    .map(AgentDelta::content);

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
                                        request.message(),
                                        "agent-plan-execute-fallback"
                                )
                                .switchIfEmpty(Flux.just("未获取到模型输出，请稍后重试。"))
                                .onErrorResume(inner -> Flux.just("模型调用失败，请稍后重试。"))
                                .map(AgentDelta::content),
                        Flux.just(AgentDelta.finish("stop"))
                ));
    }

    private Flux<AgentDelta> reactFlow(AgentRequest request) {
        return Flux.concat(
                        Flux.just(AgentDelta.thinking("进入 RE-ACT 模式，正在逐步决策...\n")),
                        Flux.defer(() -> reactLoop(request, new ArrayList<>(), 1))
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
                                        request.message(),
                                        "agent-react-fallback"
                                )
                                .switchIfEmpty(Flux.just("未获取到模型输出，请稍后重试。"))
                                .onErrorResume(inner -> Flux.just("模型调用失败，请稍后重试。"))
                                .map(AgentDelta::content),
                        Flux.just(AgentDelta.finish("stop"))
                ));
    }

    private Flux<AgentDelta> reactLoop(AgentRequest request, List<Map<String, Object>> toolRecords, int step) {
        if (step > MAX_REACT_STEPS) {
            return finalizeReactAnswer(request, toolRecords, "达到 RE-ACT 最大轮次，转为总结输出。", "agent-react-final-max");
        }

        String reactPrompt = buildReactPrompt(request, toolRecords, step);
        log.info("[agent:{}] react step={} prompt:\n{}", id(), step, reactPrompt);

        return llmService.completeText(
                        providerType(),
                        model(),
                        reactSystemPrompt(),
                        reactPrompt,
                        "agent-react-step-" + step
                )
                .flatMapMany(raw -> {
                    ReactDecision decision = parseReactDecision(raw);
                    log.info("[agent:{}] react step={} raw decision:\n{}", id(), step, raw);
                    log.info("[agent:{}] react step={} parsed decision={}", id(), step, toJson(decision));

                    List<AgentDelta> stepDeltas = new ArrayList<>();
                    if (!decision.thinking().isBlank()) {
                        stepDeltas.add(AgentDelta.thinking(decision.thinking()));
                    }

                    if (decision.done()) {
                        return Flux.concat(
                                Flux.fromIterable(stepDeltas),
                                finalizeReactAnswer(
                                        request,
                                        toolRecords,
                                        "决策完成，正在流式生成最终回答。",
                                        "agent-react-final-step-" + step
                                )
                        );
                    }

                    if (decision.action() == null) {
                        return Flux.concat(
                                Flux.fromIterable(stepDeltas),
                                finalizeReactAnswer(request, toolRecords, "未获得可执行 action，转为总结输出。", "agent-react-final-empty")
                        );
                    }

                    ToolExecution execution = executeSingleTool(decision.action(), step);
                    toolRecords.addAll(execution.records());

                    return Flux.concat(
                            Flux.fromIterable(stepDeltas),
                            Flux.fromIterable(execution.deltas()),
                            reactLoop(request, toolRecords, step + 1)
                    );
                });
    }

    private Flux<AgentDelta> finalizeReactAnswer(
            AgentRequest request,
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
        List<String> planSteps = readTextArray(root.path("plan"));

        List<PlannedToolCall> toolCalls = new ArrayList<>();
        JsonNode toolCallsNode = root.path("toolCalls");
        if (toolCallsNode.isArray()) {
            for (JsonNode callNode : toolCallsNode) {
                PlannedToolCall call = readPlannedToolCall(callNode);
                if (call != null) {
                    toolCalls.add(call);
                }
            }
        }

        return new PlannerDecision(thinking, planSteps, toolCalls);
    }

    private PlannerDecision fallbackPlannerDecision(String rawPlan) {
        String thinking = "根据用户问题生成计划，按需调用工具，最后输出可执行结论。";
        if (rawPlan != null && !rawPlan.isBlank()) {
            thinking += " 原始规划输出无法解析，已降级为无工具执行。";
        }

        return new PlannerDecision(
                thinking,
                List.of("确认用户目标与输入约束", "判断是否需要工具辅助", "输出结论与下一步建议"),
                List.of()
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

    private ToolExecution executePlannedTools(PlannerDecision decision) {
        List<AgentDelta> deltas = new ArrayList<>();
        List<Map<String, Object>> records = new ArrayList<>();

        int index = 1;
        for (PlannedToolCall plannedCall : decision.toolCalls()) {
            if (index > MAX_TOOL_CALLS) {
                break;
            }
            ToolExecution execution = executeTool(plannedCall, "call_" + sanitize(plannedCall.name()) + "_" + index);
            deltas.addAll(execution.deltas());
            records.addAll(execution.records());
            index++;
        }

        return new ToolExecution(deltas, records);
    }

    private ToolExecution executeSingleTool(PlannedToolCall plannedCall, int step) {
        return executeTool(plannedCall, "call_" + sanitize(plannedCall.name()) + "_step_" + step);
    }

    private ToolExecution executeTool(PlannedToolCall plannedCall, String callId) {
        Map<String, Object> args = new LinkedHashMap<>();
        if (plannedCall.arguments() != null) {
            args.putAll(plannedCall.arguments());
        }

        List<AgentDelta> deltas = new ArrayList<>();
        deltas.add(AgentDelta.toolCalls(List.of(toolCall(callId, plannedCall.name(), toJson(args)))));

        JsonNode result = safeInvoke(plannedCall.name(), args);
        deltas.add(AgentDelta.toolResult(callId, result));

        Map<String, Object> record = new LinkedHashMap<>();
        record.put("callId", callId);
        record.put("toolName", plannedCall.name());
        record.put("arguments", args);
        record.put("result", result);

        return new ToolExecution(deltas, List.of(record));
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

        if (!decision.plan().isEmpty()) {
            builder.append("\n计划：");
            int i = 1;
            for (String step : decision.plan()) {
                builder.append("\n").append(i++).append(". ").append(step);
            }
        }

        if (!decision.toolCalls().isEmpty()) {
            builder.append("\n计划工具调用：");
            List<String> names = decision.toolCalls().stream().map(PlannedToolCall::name).toList();
            builder.append(String.join(", ", names));
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
                  "plan": ["步骤1", "步骤2"],
                  "toolCalls": [{"name": "tool_name", "arguments": {"k": "v"}}]
                }

                约束：
                1) thinking 用中文，一句话。
                2) plan 输出 1-4 条可执行步骤。
                3) toolCalls 只在必要时填写，最多 %d 个；name 必须来自工具列表。
                %s
                5) arguments 必须显式给出且符合工具定义，不要依赖任何隐式参数补齐。
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

    private String plannerSystemPrompt() {
        return normalize(systemPrompt(), "你是通用助理")
                + "\n你当前处于任务编排阶段：先深度思考，再给出计划，并按需声明工具调用。";
    }

    private String reactSystemPrompt() {
        return normalize(systemPrompt(), "你是通用助理")
                + "\n你当前处于 RE-ACT 阶段：每轮只做一个动作决策（继续调用工具或直接给最终回答）。";
    }

    private String buildPlanExecuteFinalPrompt(
            AgentRequest request,
            PlannerDecision decision,
            List<Map<String, Object>> toolRecords
    ) {
        String toolResultJson = toJson(toolRecords);
        String planText = decision.plan().isEmpty() ? "[]" : String.join(" | ", decision.plan());

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

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    private record PlannerDecision(
            String thinking,
            List<String> plan,
            List<PlannedToolCall> toolCalls
    ) {
    }

    private record ReactDecision(
            String thinking,
            PlannedToolCall action,
            boolean done
    ) {
    }

    private record PlannedToolCall(
            String name,
            Map<String, Object> arguments
    ) {
    }

    private record ToolExecution(
            List<AgentDelta> deltas,
            List<Map<String, Object>> records
    ) {
    }
}
