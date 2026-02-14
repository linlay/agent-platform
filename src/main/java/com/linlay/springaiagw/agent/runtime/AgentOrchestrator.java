package com.linlay.springaiagw.agent.runtime;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.AgentDefinition;
import com.linlay.springaiagw.agent.PlannedToolCall;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.ai.chat.messages.UserMessage;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.FluxSink;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

public class AgentOrchestrator {

    private static final Logger log = LoggerFactory.getLogger(AgentOrchestrator.class);
    private static final TypeReference<Map<String, Object>> MAP_TYPE = new TypeReference<>() {
    };

    private final LlmService llmService;
    private final ToolExecutionService toolExecutionService;
    private final VerifyService verifyService;
    private final ObjectMapper objectMapper;

    public AgentOrchestrator(
            LlmService llmService,
            ToolExecutionService toolExecutionService,
            VerifyService verifyService,
            ObjectMapper objectMapper
    ) {
        this.llmService = llmService;
        this.toolExecutionService = toolExecutionService;
        this.verifyService = verifyService;
        this.objectMapper = objectMapper;
    }

    public Flux<AgentDelta> runStream(
            AgentDefinition definition,
            AgentRequest request,
            List<Message> historyMessages,
            Map<String, BaseTool> enabledToolsByName
    ) {
        return Flux.create(sink -> {
            ExecutionContext context = new ExecutionContext(definition, request, historyMessages);
            RunSpec spec = definition.runSpec();

            try {
                switch (spec.control()) {
                    case ONESHOT -> runOneShot(context, enabledToolsByName, sink);
                    case TOOL_ONESHOT -> runToolOneShot(context, enabledToolsByName, sink);
                    case REACT_LOOP -> runReactLoop(context, enabledToolsByName, sink);
                    case PLAN_EXECUTE -> runPlanExecute(context, enabledToolsByName, sink);
                }

                emit(sink, AgentDelta.finish("stop"));
                if (!sink.isCancelled()) {
                    sink.complete();
                }
            } catch (Exception ex) {
                log.warn("[agent:{}] orchestration failed", definition.id(), ex);
                emit(sink, AgentDelta.content("模型调用失败，请稍后重试。"));
                emit(sink, AgentDelta.finish("stop"));
                if (!sink.isCancelled()) {
                    sink.complete();
                }
            }
        }, FluxSink.OverflowStrategy.BUFFER);
    }

    private void runOneShot(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            FluxSink<AgentDelta> sink
    ) {
        boolean isThinking = context.definition().mode() == AgentRuntimeMode.THINKING;
        boolean emitReasoning = isThinking && context.definition().runSpec().exposeReasoningToUser();
        boolean secondPass = verifyService.requiresSecondPass(context.definition().runSpec().verify());

        ModelTurn turn = callModelTurnStreaming(
                context,
                context.definition().promptSet().systemPrompt(),
                context.conversationMessages(),
                null,
                List.of(),
                ToolChoice.NONE,
                isThinking ? "agent-thinking-oneshot" : "agent-plain-oneshot",
                false,
                emitReasoning,
                !secondPass,
                true,
                sink
        );
        String finalText = normalize(turn.finalText());
        appendAssistantMessage(context.conversationMessages(), finalText);
        emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }

    private void runToolOneShot(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            FluxSink<AgentDelta> sink
    ) {
        boolean isThinkingTooling = context.definition().mode() == AgentRuntimeMode.THINKING_TOOLING;
        boolean emitReasoning = isThinkingTooling && context.definition().runSpec().exposeReasoningToUser();

        ToolChoice toolChoice = requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO;
        ModelTurn firstTurn = callModelTurnStreaming(
                context,
                context.definition().promptSet().systemPrompt(),
                context.conversationMessages(),
                null,
                toolExecutionService.enabledFunctionTools(enabledToolsByName),
                toolChoice,
                "agent-tooling-first",
                false,
                emitReasoning,
                true,
                true,
                sink
        );

        if (firstTurn.toolCalls().isEmpty() && requiresTool(context)) {
            context.conversationMessages().add(new UserMessage(
                    "你必须调用至少一个工具来完成任务。请重新选择工具并发起调用。"
            ));
            firstTurn = callModelTurnStreaming(
                    context,
                    context.definition().promptSet().systemPrompt(),
                    context.conversationMessages(),
                    null,
                    toolExecutionService.enabledFunctionTools(enabledToolsByName),
                    ToolChoice.REQUIRED,
                    "agent-tooling-first-repair",
                    false,
                    emitReasoning,
                    true,
                    true,
                    sink
            );
        }

        if (firstTurn.toolCalls().isEmpty()) {
            if (requiresTool(context)) {
                log.warn("[agent:{}] ToolPolicy.REQUIRE violated in TOOL_ONESHOT: no tool call produced",
                        context.definition().id());
            }
            String finalText = normalize(firstTurn.finalText());
            appendAssistantMessage(context.conversationMessages(), finalText);
            emitFinalAnswer(context, context.conversationMessages(), finalText, true, sink);
            return;
        }

        executeToolsAndEmit(context, enabledToolsByName, firstTurn.toolCalls(), sink);

        boolean secondPass = verifyService.requiresSecondPass(context.definition().runSpec().verify());
        ModelTurn secondTurn = callModelTurnStreaming(
                context,
                context.definition().promptSet().systemPrompt(),
                context.conversationMessages(),
                "请基于已有信息输出最终答案，不再调用工具。",
                List.of(),
                ToolChoice.NONE,
                isThinkingTooling ? "agent-thinking-tooling-final" : "agent-tooling-final",
                false,
                emitReasoning,
                !secondPass,
                true,
                sink
        );
        String finalText = normalize(secondTurn.finalText());
        appendAssistantMessage(context.conversationMessages(), finalText);
        emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }

    private void runReactLoop(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            FluxSink<AgentDelta> sink
    ) {
        int maxSteps = context.budget().maxSteps();
        if (context.definition().runSpec().budget().maxSteps() > 0) {
            maxSteps = context.definition().runSpec().budget().maxSteps();
        }

        for (int step = 1; step <= maxSteps; step++) {
            ModelTurn turn = callModelTurnStreaming(
                    context,
                    context.definition().promptSet().systemPrompt(),
                    context.conversationMessages(),
                    null,
                    toolExecutionService.enabledFunctionTools(enabledToolsByName),
                    requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    "agent-react-step-" + step,
                    false,
                    false,
                    true,
                    true,
                    sink
            );

            if (!turn.toolCalls().isEmpty()) {
                executeToolsAndEmit(context, enabledToolsByName, turn.toolCalls(), sink);
                continue;
            }

            if (requiresTool(context)) {
                context.conversationMessages().add(new UserMessage(
                        "你必须调用至少一个工具来继续。请直接发起工具调用。"
                ));
                continue;
            }

            String finalText = normalize(turn.finalText());
            if (finalText.isBlank()) {
                context.conversationMessages().add(new UserMessage("请基于已有信息给出最终答案，或调用工具获取更多信息。"));
                continue;
            }

            appendAssistantMessage(context.conversationMessages(), finalText);
            emitFinalAnswer(context, context.conversationMessages(), finalText, true, sink);
            return;
        }

        boolean secondPass = verifyService.requiresSecondPass(context.definition().runSpec().verify());
        String forced = forceFinalAnswer(
                context,
                context.definition().promptSet().systemPrompt(),
                context.conversationMessages(),
                "agent-react-force-final",
                !secondPass,
                sink
        );
        appendAssistantMessage(context.conversationMessages(), forced);
        emitFinalAnswer(context, context.conversationMessages(), forced, !secondPass, sink);
    }

    private void runPlanExecute(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            FluxSink<AgentDelta> sink
    ) {
        String planPrompt = context.definition().promptSet().planSystemPrompt();
        String executePrompt = context.definition().promptSet().executeSystemPrompt();
        String summaryPrompt = context.definition().promptSet().summarySystemPrompt();
        if (summaryPrompt == null || summaryPrompt.isBlank()) {
            summaryPrompt = executePrompt;
        }

        ModelTurn planTurn = callModelTurnStreaming(
                context,
                planPrompt,
                context.planMessages(),
                "请输出结构化计划（JSON），包含 steps 字段，每个 step 含 title、goal、successCriteria。",
                List.of(),
                ToolChoice.NONE,
                "agent-plan-generate",
                false,
                false,
                true,
                true,
                sink
        );
        List<PlanStep> steps = parsePlanSteps(planTurn.finalText());
        if (steps.isEmpty()) {
            steps = List.of(new PlanStep("step-1", "执行任务", context.request().message(), "输出可执行结果"));
        }

        int stepNo = 0;
        for (PlanStep step : steps) {
            stepNo++;
            if (stepNo > context.budget().maxSteps()) {
                break;
            }
            context.executeMessages().add(new UserMessage(
                    "当前执行步骤 [" + stepNo + "/" + steps.size() + "]: " + step.title()
                            + "\n目标: " + step.goal()
                            + "\n成功标准: " + step.successCriteria()
            ));

            ModelTurn stepTurn = callModelTurnStreaming(
                    context,
                    executePrompt,
                    context.executeMessages(),
                    null,
                    toolExecutionService.enabledFunctionTools(enabledToolsByName),
                    requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    "agent-plan-execute-step-" + stepNo,
                    true,
                    false,
                    true,
                    true,
                    sink
            );

            if (stepTurn.toolCalls().isEmpty() && requiresTool(context)) {
                context.executeMessages().add(new UserMessage(
                        "你必须在该步骤中使用工具。请重新调用至少一个工具。"
                ));
                stepTurn = callModelTurnStreaming(
                        context,
                        executePrompt,
                        context.executeMessages(),
                        null,
                        toolExecutionService.enabledFunctionTools(enabledToolsByName),
                        ToolChoice.REQUIRED,
                        "agent-plan-execute-step-" + stepNo + "-repair",
                        true,
                        false,
                        true,
                        true,
                        sink
                );
            }

            if (!stepTurn.toolCalls().isEmpty()) {
                executeToolsAndEmit(context, enabledToolsByName, stepTurn.toolCalls(), sink);

                ModelTurn stepSummary = callModelTurnStreaming(
                        context,
                        executePrompt,
                        context.executeMessages(),
                        "请总结当前步骤执行结果。",
                        List.of(),
                        ToolChoice.NONE,
                        "agent-plan-step-summary-" + stepNo,
                        false,
                        false,
                        true,
                        true,
                        sink
                );
                String summary = normalize(stepSummary.finalText());
                appendAssistantMessage(context.executeMessages(), summary);
                if (!summary.isBlank()) {
                    context.toolRecords().add(Map.of(
                            "stepId", step.id(),
                            "stepTitle", step.title(),
                            "summary", summary
                    ));
                }
            } else if (!normalize(stepTurn.finalText()).isBlank()) {
                appendAssistantMessage(context.executeMessages(), normalize(stepTurn.finalText()));
                context.toolRecords().add(Map.of(
                        "stepId", step.id(),
                        "stepTitle", step.title(),
                        "summary", normalize(stepTurn.finalText())
                ));
            }
        }

        context.executeMessages().add(new UserMessage("所有步骤已完成，请综合所有步骤的执行结果给出最终答案。"));
        boolean secondPass = verifyService.requiresSecondPass(context.definition().runSpec().verify());

        String finalText = forceFinalAnswer(context, summaryPrompt, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        appendAssistantMessage(context.executeMessages(), finalText);
        emitFinalAnswer(context, context.executeMessages(), finalText, !secondPass, sink);
    }

    private void emitFinalAnswer(
            ExecutionContext context,
            List<Message> messages,
            String candidateFinalText,
            boolean contentAlreadyEmitted,
            FluxSink<AgentDelta> sink
    ) {
        VerifyPolicy verifyPolicy = context.definition().runSpec().verify();
        boolean secondPass = verifyService.requiresSecondPass(verifyPolicy);

        if (!secondPass) {
            if (!contentAlreadyEmitted && StringUtils.hasText(candidateFinalText)) {
                emit(sink, AgentDelta.content(candidateFinalText));
            }
            return;
        }

        if (!StringUtils.hasText(candidateFinalText)) {
            return;
        }
        StringBuilder verifyOutput = new StringBuilder();
        for (String chunk : verifyService.streamSecondPass(
                verifyPolicy,
                context.definition().providerKey(),
                context.definition().model(),
                context.definition().promptSet().primarySystemPrompt(),
                messages,
                candidateFinalText,
                "agent-verify"
        ).toIterable()) {
            if (!StringUtils.hasText(chunk)) {
                continue;
            }
            verifyOutput.append(chunk);
            emit(sink, AgentDelta.content(chunk));
        }

        if (verifyOutput.isEmpty() && !contentAlreadyEmitted) {
            emit(sink, AgentDelta.content(candidateFinalText));
        }
    }

    private String forceFinalAnswer(
            ExecutionContext context,
            String systemPrompt,
            List<Message> messages,
            String stage,
            boolean emitContent,
            FluxSink<AgentDelta> sink
    ) {
        ModelTurn turn = callModelTurnStreaming(
                context,
                systemPrompt,
                messages,
                "请基于当前信息输出最终答案，不再调用工具。",
                List.of(),
                ToolChoice.NONE,
                stage,
                false,
                false,
                emitContent,
                true,
                sink
        );
        return normalize(turn.finalText());
    }

    private ModelTurn callModelTurnStreaming(
            ExecutionContext context,
            String systemPrompt,
            List<Message> messages,
            String userPrompt,
            List<LlmService.LlmFunctionTool> tools,
            ToolChoice toolChoice,
            String stage,
            boolean parallelToolCalls,
            boolean emitReasoning,
            boolean emitContent,
            boolean emitToolCalls,
            FluxSink<AgentDelta> sink
    ) {
        context.incrementModelCalls();

        StringBuilder reasoning = new StringBuilder();
        StringBuilder content = new StringBuilder();
        Map<String, ToolAccumulator> toolsById = new LinkedHashMap<>();
        ToolAccumulator latest = null;
        int seq = 0;

        for (LlmDelta delta : llmService.streamDeltas(new LlmCallSpec(
                context.definition().providerKey(),
                context.definition().model(),
                systemPrompt,
                messages,
                userPrompt,
                tools,
                toolChoice,
                null,
                null,
                context.definition().runSpec().compute(),
                4096,
                stage,
                parallelToolCalls
        )).toIterable()) {
            if (delta == null) {
                continue;
            }

            if (StringUtils.hasText(delta.reasoning())) {
                reasoning.append(delta.reasoning());
                if (emitReasoning) {
                    emit(sink, AgentDelta.thinking(delta.reasoning()));
                }
            }

            if (StringUtils.hasText(delta.content())) {
                content.append(delta.content());
                if (emitContent) {
                    emit(sink, AgentDelta.content(delta.content()));
                }
            }

            List<ToolCallDelta> streamedCalls = new ArrayList<>();
            if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
                for (ToolCallDelta call : delta.toolCalls()) {
                    if (call == null) {
                        continue;
                    }
                    String callId = normalize(call.id());
                    if (!StringUtils.hasText(callId)) {
                        callId = latest == null ? "call_native_" + (++seq) : latest.callId;
                    }

                    ToolAccumulator acc = toolsById.computeIfAbsent(callId, ToolAccumulator::new);
                    if (StringUtils.hasText(call.name())) {
                        acc.toolName = call.name();
                    }
                    if (StringUtils.hasText(call.type())) {
                        acc.toolType = call.type();
                    }
                    if (StringUtils.hasText(call.arguments())) {
                        acc.arguments.append(call.arguments());
                    }
                    latest = acc;

                    if (!emitToolCalls || !StringUtils.hasText(call.arguments())) {
                        continue;
                    }
                    String emittedName = StringUtils.hasText(call.name()) ? call.name() : acc.toolName;
                    String emittedType = StringUtils.hasText(call.type())
                            ? call.type()
                            : (StringUtils.hasText(acc.toolType) ? acc.toolType : "function");
                    streamedCalls.add(new ToolCallDelta(callId, emittedType, emittedName, call.arguments()));
                }
            }
            if (!streamedCalls.isEmpty()) {
                emit(sink, AgentDelta.toolCalls(streamedCalls));
            }
        }

        List<PlannedToolCall> plannedToolCalls = new ArrayList<>();
        for (ToolAccumulator acc : toolsById.values()) {
            String toolName = normalize(acc.toolName).toLowerCase(Locale.ROOT);
            if (!StringUtils.hasText(toolName)) {
                continue;
            }
            Map<String, Object> args = parseMap(acc.arguments.toString());
            plannedToolCalls.add(new PlannedToolCall(toolName, args, acc.callId));
        }

        return new ModelTurn(content.toString(), reasoning.toString(), plannedToolCalls);
    }

    private void executeToolsAndEmit(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            List<PlannedToolCall> plannedToolCalls,
            FluxSink<AgentDelta> sink
    ) {
        ToolExecutionService.ToolExecutionBatch batch = toolExecutionService.executeToolCalls(
                plannedToolCalls,
                enabledToolsByName,
                context.toolRecords(),
                context.request().runId(),
                false
        );
        context.incrementToolCalls(batch.events().size());
        for (AgentDelta delta : batch.deltas()) {
            emit(sink, delta);
        }
        appendToolEvents(context.conversationMessages(), batch.events());
        appendToolEvents(context.executeMessages(), batch.events());
    }

    private void appendToolEvents(List<Message> messages, List<ToolExecutionService.ToolExecutionEvent> events) {
        for (ToolExecutionService.ToolExecutionEvent event : events) {
            AssistantMessage.ToolCall toolCall = new AssistantMessage.ToolCall(
                    event.callId(),
                    event.toolType(),
                    event.toolName(),
                    event.argsJson()
            );
            messages.add(new AssistantMessage("", Map.of(), List.of(toolCall)));

            ToolResponseMessage.ToolResponse toolResponse = new ToolResponseMessage.ToolResponse(
                    event.callId(),
                    event.toolName(),
                    event.resultText()
            );
            messages.add(new ToolResponseMessage(List.of(toolResponse)));
        }
    }

    private void appendAssistantMessage(List<Message> messages, String text) {
        String normalized = normalize(text);
        if (!normalized.isBlank()) {
            messages.add(new AssistantMessage(normalized));
        }
    }

    private List<PlanStep> parsePlanSteps(String raw) {
        JsonNode root = readJson(raw);
        if (root == null || !root.isObject() || !root.path("steps").isArray()) {
            return List.of();
        }

        List<PlanStep> steps = new ArrayList<>();
        int index = 0;
        for (JsonNode node : root.path("steps")) {
            index++;
            String id = normalize(node.path("id").asText("step-" + index));
            String title = normalize(node.path("title").asText("Step " + index));
            String goal = normalize(node.path("goal").asText(title));
            String success = normalize(node.path("successCriteria").asText("完成步骤"));
            steps.add(new PlanStep(id, title, goal, success));
        }
        return steps;
    }

    private JsonNode readJson(String raw) {
        if (!StringUtils.hasText(raw)) {
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
            if (start >= 0 && end > start) {
                try {
                    return objectMapper.readTree(normalized.substring(start, end + 1));
                } catch (Exception ignored) {
                    return null;
                }
            }
            return null;
        }
    }

    private Map<String, Object> parseMap(String raw) {
        if (!StringUtils.hasText(raw)) {
            return new LinkedHashMap<>();
        }
        try {
            JsonNode node = objectMapper.readTree(raw);
            if (!node.isObject()) {
                return new LinkedHashMap<>();
            }
            Map<String, Object> mapped = objectMapper.convertValue(node, MAP_TYPE);
            return mapped == null ? new LinkedHashMap<>() : new LinkedHashMap<>(mapped);
        } catch (Exception ex) {
            return new LinkedHashMap<>();
        }
    }

    private String normalize(String value) {
        return value == null ? "" : value.trim();
    }

    private boolean requiresTool(ExecutionContext context) {
        return context.definition().runSpec().toolPolicy() == ToolPolicy.REQUIRE;
    }

    private void emit(FluxSink<AgentDelta> sink, AgentDelta delta) {
        if (delta == null || sink.isCancelled()) {
            return;
        }
        sink.next(delta);
    }

    private static final class ToolAccumulator {
        private final String callId;
        private String toolName;
        private String toolType;
        private final StringBuilder arguments = new StringBuilder();

        private ToolAccumulator(String callId) {
            this.callId = callId;
        }
    }

    private record ModelTurn(
            String finalText,
            String reasoningText,
            List<PlannedToolCall> toolCalls
    ) {
    }

    private record PlanStep(
            String id,
            String title,
            String goal,
            String successCriteria
    ) {
    }
}
