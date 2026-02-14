package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.*;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class PlainToolingMode extends AgentMode {

    private static final Logger log = LoggerFactory.getLogger(PlainToolingMode.class);

    public PlainToolingMode(String systemPrompt) {
        super(systemPrompt);
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.PLAIN_TOOLING;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        return new RunSpec(
                ControlStrategy.TOOL_ONESHOT,
                OutputPolicy.PLAIN,
                PlainMode.chooseToolPolicy(config, ToolPolicy.ALLOW),
                PlainMode.chooseVerify(config, VerifyPolicy.NONE),
                PlainMode.chooseCompute(config, ComputePolicy.MEDIUM),
                false,
                PlainMode.chooseBudget(config)
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        ToolChoice toolChoice = services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO;
        OrchestratorServices.ModelTurn firstTurn = services.callModelTurnStreaming(
                context,
                systemPrompt,
                context.conversationMessages(),
                null,
                services.toolExecutionService().enabledFunctionTools(enabledToolsByName),
                toolChoice,
                "agent-tooling-first",
                false,
                false,
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
                    systemPrompt,
                    context.conversationMessages(),
                    null,
                    services.toolExecutionService().enabledFunctionTools(enabledToolsByName),
                    ToolChoice.REQUIRED,
                    "agent-tooling-first-repair",
                    false,
                    false,
                    true,
                    true,
                    sink
            );
        }

        if (firstTurn.toolCalls().isEmpty()) {
            if (services.requiresTool(context)) {
                log.warn("[agent:{}] ToolPolicy.REQUIRE violated in TOOL_ONESHOT: no tool call produced",
                        context.definition().id());
            }
            String finalText = services.normalize(firstTurn.finalText());
            services.appendAssistantMessage(context.conversationMessages(), finalText);
            services.emitFinalAnswer(context, context.conversationMessages(), finalText, true, sink);
            return;
        }

        services.executeToolsAndEmit(context, enabledToolsByName, firstTurn.toolCalls(), sink);

        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());
        OrchestratorServices.ModelTurn secondTurn = services.callModelTurnStreaming(
                context,
                systemPrompt,
                context.conversationMessages(),
                "请基于已有信息输出最终答案，不再调用工具。",
                List.of(),
                ToolChoice.NONE,
                "agent-tooling-final",
                false,
                false,
                !secondPass,
                true,
                sink
        );
        String finalText = services.normalize(secondTurn.finalText());
        services.appendAssistantMessage(context.conversationMessages(), finalText);
        services.emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }
}
