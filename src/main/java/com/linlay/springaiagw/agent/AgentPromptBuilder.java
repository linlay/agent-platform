package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.tool.BaseTool;

import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * 构建各模式（PLAIN / RE_ACT / PLAN_EXECUTE）的 system prompt 和 user prompt。
 */
class AgentPromptBuilder {

    private static final int MAX_REACT_STEPS = 6;

    private final ObjectMapper objectMapper;
    private final String systemPrompt;
    private final Map<String, BaseTool> enabledToolsByName;

    AgentPromptBuilder(ObjectMapper objectMapper, String systemPrompt, Map<String, BaseTool> enabledToolsByName) {
        this.objectMapper = objectMapper;
        this.systemPrompt = systemPrompt;
        this.enabledToolsByName = enabledToolsByName;
    }

    String buildPlanExecuteLoopPrompt(AgentRequest request, List<Map<String, Object>> toolRecords, int step) {
        return """
                用户问题：%s
                当前轮次：%d/%d
                历史工具结果(JSON)：%s
                可用工具：
                %s

                请按 OpenAI 原生 Function Calling 协议工作：
                1) 需要继续查证时，直接发起 tool_calls，可在同一轮中调用多个工具（支持并行）。
                2) 已可直接回答时，不再调用工具，直接输出最终结论文本。
                3) 工具参数必须严格匹配工具 schema，避免缺省或隐式参数。
                4) 若参数依赖时间，不要传 today/tomorrow/昨天/明天，优先传具体日期（YYYY-MM-DD）或先调用时间工具再传日期。
                5) bash.command 必须是单条命令，不允许 &&、||、;、管道。
                """.formatted(
                request.message(), step, MAX_REACT_STEPS, toJson(toolRecords), enabledToolsPrompt()
        );
    }
    String buildReactPrompt(AgentRequest request, List<Map<String, Object>> toolRecords, int step) {
        return """
                用户问题：%s
                当前轮次：%d/%d
                历史工具结果(JSON)：%s
                可用工具：
                %s

                请按 OpenAI 原生 Function Calling 协议工作：
                1) 需要继续查证时，直接发起 tool_calls，不要在正文输出 action/toolCall JSON。
                2) 已可直接回答时，不再调用工具，直接输出最终结论文本。
                3) 每轮最多一个工具调用。
                4) 参数必须严格符合工具 schema。
                """.formatted(
                request.message(), step, MAX_REACT_STEPS, toJson(toolRecords), enabledToolsPrompt()
        );
    }

    String buildPlainDecisionPrompt(AgentRequest request) {
        return """
                用户问题：%s
                可用工具：
                %s

                请按 OpenAI 原生 Function Calling 协议工作：
                1) 需要工具时，直接发起 tool_calls，不要在正文输出 toolCall/toolCalls JSON。
                2) 此模式最多允许一个工具调用。
                3) 不需要工具时，直接输出最终回答文本。
                4) 参数必须严格符合工具 schema。
                """.formatted(
                request.message(), enabledToolsPrompt()
        );
    }

    String planExecuteLoopSystemPrompt() {
        return runtimeSystemPrompt()
                + "\n你当前处于 PLAN-EXECUTE 循环阶段：每轮可调用一个或多个工具（支持并行），执行完成后再决策下一步。";
    }

    String reactSystemPrompt() {
        return runtimeSystemPrompt()
                + "\n你当前处于 RE-ACT 阶段：每轮只做一个动作决策（继续调用工具或直接给最终回答）。";
    }

    String plainDecisionSystemPrompt() {
        return runtimeSystemPrompt()
                + "\n你当前处于 PLAIN 单工具决策阶段：先判断是否需要工具；若需要，只能调用一个工具。";
    }

    String runtimeSystemPrompt() {
        String base = normalize(systemPrompt, "你是通用助理");
        String toolPrompts = enabledToolPrompts();
        if (toolPrompts.isBlank()) {
            return base;
        }
        return base
                + "\n\n工具附加提示（按需遵循）：\n"
                + toolPrompts
                + "\n仅当需要渲染 viewport 时遵循对应工具的 type/key 约束。";
    }

    String buildPlanExecuteLoopFinalPrompt(AgentRequest request, List<Map<String, Object>> toolRecords) {
        return """
                用户问题：%s
                工具执行结果(JSON)：%s

                请基于以上信息输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 必要时给简短行动建议。
                4) 保持简洁、可执行。
                """.formatted(request.message(), toJson(toolRecords));
    }

    String buildReactFinalPrompt(AgentRequest request, List<Map<String, Object>> toolRecords) {
        return """
                用户问题：%s
                工具执行结果(JSON)：%s

                请输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 必要时给简短行动建议。
                4) 保持简洁、可执行。
                """.formatted(request.message(), toJson(toolRecords));
    }

    String buildPlainFinalPrompt(AgentRequest request, List<Map<String, Object>> toolRecords) {
        return """
                用户问题：%s
                工具执行结果(JSON)：%s

                请输出最终回答：
                1) 先给结论。
                2) 若有工具结果，引用关键结果再总结。
                3) 保持简洁、可执行。
                """.formatted(request.message(), toJson(toolRecords));
    }

    String enabledToolsPrompt() {
        if (enabledToolsByName.isEmpty()) {
            return "- 无可用工具";
        }
        return enabledToolsByName.values().stream()
                .sorted(Comparator.comparing(BaseTool::name))
                .map(tool -> "- " + tool.name() + "：" + tool.description())
                .reduce((left, right) -> left + "\n" + right)
                .orElse("- 无可用工具");
    }

    private String enabledToolPrompts() {
        if (enabledToolsByName.isEmpty()) {
            return "";
        }
        return enabledToolsByName.values().stream()
                .sorted(Comparator.comparing(BaseTool::name))
                .map(tool -> {
                    String prompt = normalize(tool.prompt(), "");
                    if (prompt.isBlank()) {
                        return null;
                    }
                    return "- " + tool.name() + "：" + prompt;
                })
                .filter(Objects::nonNull)
                .reduce((left, right) -> left + "\n" + right)
                .orElse("");
    }

    String toJson(Object value) {
        try {
            return objectMapper.writeValueAsString(value);
        } catch (JsonProcessingException ex) {
            throw new IllegalStateException("Cannot serialize json", ex);
        }
    }

    private static String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }
}
