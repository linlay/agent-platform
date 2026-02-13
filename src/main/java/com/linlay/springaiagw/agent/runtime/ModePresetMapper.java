package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;

public final class ModePresetMapper {

    private ModePresetMapper() {
    }

    public static RunSpec toRunSpec(AgentRuntimeMode mode, AgentConfigFile config) {
        if (mode == null) {
            throw new IllegalArgumentException("mode is required");
        }

        Budget budget = config != null && config.getBudget() != null
                ? config.getBudget().toBudget()
                : Budget.DEFAULT;

        return switch (mode) {
            case PLAIN -> new RunSpec(
                    ControlStrategy.ONESHOT,
                    OutputPolicy.PLAIN,
                    ToolPolicy.DISALLOW,
                    VerifyPolicy.NONE,
                    chooseCompute(config, ComputePolicy.LOW),
                    false,
                    budget
            );
            case THINKING -> new RunSpec(
                    ControlStrategy.ONESHOT,
                    OutputPolicy.REASONING_SUMMARY,
                    ToolPolicy.DISALLOW,
                    chooseVerify(config, VerifyPolicy.NONE),
                    chooseCompute(config, ComputePolicy.MEDIUM),
                    config != null
                            && config.getThinking() != null
                            && Boolean.TRUE.equals(config.getThinking().getExposeReasoningToUser()),
                    budget
            );
            case PLAIN_TOOLING -> new RunSpec(
                    ControlStrategy.TOOL_ONESHOT,
                    OutputPolicy.PLAIN,
                    chooseToolPolicy(config, ToolPolicy.ALLOW),
                    chooseVerify(config, VerifyPolicy.NONE),
                    chooseCompute(config, ComputePolicy.MEDIUM),
                    false,
                    budget
            );
            case THINKING_TOOLING -> new RunSpec(
                    ControlStrategy.TOOL_ONESHOT,
                    OutputPolicy.REASONING_SUMMARY,
                    chooseToolPolicy(config, ToolPolicy.ALLOW),
                    chooseVerify(config, VerifyPolicy.NONE),
                    chooseCompute(config, ComputePolicy.MEDIUM),
                    config != null
                            && config.getThinkingTooling() != null
                            && Boolean.TRUE.equals(config.getThinkingTooling().getExposeReasoningToUser()),
                    budget
            );
            case REACT -> new RunSpec(
                    ControlStrategy.REACT_LOOP,
                    chooseOutput(config, OutputPolicy.PLAIN),
                    chooseToolPolicy(config, ToolPolicy.ALLOW),
                    chooseVerify(config, VerifyPolicy.NONE),
                    chooseCompute(config, ComputePolicy.MEDIUM),
                    chooseOutput(config, OutputPolicy.PLAIN) == OutputPolicy.REASONING_SUMMARY,
                    budget
            );
            case PLAN_EXECUTE -> new RunSpec(
                    ControlStrategy.PLAN_EXECUTE,
                    chooseOutput(config, OutputPolicy.PLAIN),
                    chooseToolPolicy(config, ToolPolicy.ALLOW),
                    chooseVerify(config, VerifyPolicy.SECOND_PASS_FIX),
                    chooseCompute(config, ComputePolicy.HIGH),
                    chooseOutput(config, OutputPolicy.PLAIN) == OutputPolicy.REASONING_SUMMARY,
                    budget
            );
        };
    }

    private static ComputePolicy chooseCompute(AgentConfigFile config, ComputePolicy fallback) {
        if (config == null || config.getCompute() == null) {
            return fallback;
        }
        return config.getCompute();
    }

    private static VerifyPolicy chooseVerify(AgentConfigFile config, VerifyPolicy fallback) {
        if (config == null || config.getVerify() == null) {
            return fallback;
        }
        return config.getVerify();
    }

    private static ToolPolicy chooseToolPolicy(AgentConfigFile config, ToolPolicy fallback) {
        if (config == null || config.getToolPolicy() == null) {
            return fallback;
        }
        return config.getToolPolicy();
    }

    private static OutputPolicy chooseOutput(AgentConfigFile config, OutputPolicy fallback) {
        if (config == null || config.getOutput() == null) {
            return fallback;
        }
        return config.getOutput();
    }
}
