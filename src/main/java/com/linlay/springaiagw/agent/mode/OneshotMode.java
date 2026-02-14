package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class OneshotMode extends AgentMode {

    private static final Logger log = LoggerFactory.getLogger(OneshotMode.class);

    private final StageSettings stage;

    public OneshotMode(StageSettings stage) {
        super(stage == null ? "" : stage.systemPrompt());
        this.stage = stage;
    }

    public StageSettings stage() {
        return stage;
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.ONESHOT;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        ToolPolicy defaultPolicy = stage != null && stage.tools() != null && !stage.tools().isEmpty()
                ? ToolPolicy.ALLOW
                : ToolPolicy.DISALLOW;
        return new RunSpec(
                ControlStrategy.ONESHOT,
                config != null && config.getOutput() != null ? config.getOutput() : OutputPolicy.PLAIN,
                config != null && config.getToolPolicy() != null ? config.getToolPolicy() : defaultPolicy,
                config != null && config.getVerify() != null ? config.getVerify() : VerifyPolicy.NONE,
                config != null && config.getBudget() != null ? config.getBudget().toBudget() : Budget.DEFAULT
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        StageSettings stageSettings = stage == null
                ? new StageSettings(systemPrompt, null, null, List.of(), false, null)
                : stage;
        Map<String, BaseTool> stageTools = services.selectTools(enabledToolsByName, stageSettings.tools());
        boolean hasTools = !stageTools.isEmpty();
        boolean emitReasoning = stageSettings.reasoningEnabled();

        if (!hasTools) {
            boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());
            OrchestratorServices.ModelTurn turn = services.callModelTurnStreaming(
                    context,
                    stageSettings,
                    context.conversationMessages(),
                    null,
                    List.of(),
                    ToolChoice.NONE,
                    "agent-oneshot",
                    false,
                    emitReasoning,
                    !secondPass,
                    true,
                    sink
            );
            String finalText = services.normalize(turn.finalText());
            services.appendAssistantMessage(context.conversationMessages(), finalText);
            services.emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
            return;
        }

        ToolChoice toolChoice = services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO;
        OrchestratorServices.ModelTurn firstTurn = services.callModelTurnStreaming(
                context,
                stageSettings,
                context.conversationMessages(),
                null,
                services.toolExecutionService().enabledFunctionTools(stageTools),
                toolChoice,
                "agent-oneshot-tool-first",
                false,
                emitReasoning,
                true,
                true,
                sink
        );

        if (firstTurn.toolCalls().isEmpty() && services.requiresTool(context)) {
            context.conversationMessages().add(new UserMessage(
                    "你必须调用至少一个工具来完成任务。请重新选择工具并发起调用。"
            ));
            firstTurn = services.callModelTurnStreaming(
                    context,
                    stageSettings,
                    context.conversationMessages(),
                    null,
                    services.toolExecutionService().enabledFunctionTools(stageTools),
                    ToolChoice.REQUIRED,
                    "agent-oneshot-tool-first-repair",
                    false,
                    emitReasoning,
                    true,
                    true,
                    sink
            );
        }

        if (firstTurn.toolCalls().isEmpty()) {
            if (services.requiresTool(context)) {
                log.warn("[agent:{}] ToolPolicy.REQUIRE violated in ONESHOT: no tool call produced",
                        context.definition().id());
            }
            String finalText = services.normalize(firstTurn.finalText());
            services.appendAssistantMessage(context.conversationMessages(), finalText);
            services.emitFinalAnswer(context, context.conversationMessages(), finalText, true, sink);
            return;
        }

        services.executeToolsAndEmit(context, stageTools, firstTurn.toolCalls(), sink);

        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());
        OrchestratorServices.ModelTurn secondTurn = services.callModelTurnStreaming(
                context,
                stageSettings,
                context.conversationMessages(),
                "请基于已有信息输出最终答案，不再调用工具。",
                List.of(),
                ToolChoice.NONE,
                "agent-oneshot-tool-final",
                false,
                emitReasoning,
                !secondPass,
                true,
                sink
        );
        String finalText = services.normalize(secondTurn.finalText());
        services.appendAssistantMessage(context.conversationMessages(), finalText);
        services.emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }
}
