package com.linlay.springaiagw.agent.runtime.policy;

public record RunSpec(
        ControlStrategy control,
        OutputPolicy output,
        ToolPolicy toolPolicy,
        VerifyPolicy verify,
        Budget budget
) {
    public RunSpec {
        if (control == null) {
            control = ControlStrategy.ONESHOT;
        }
        if (output == null) {
            output = OutputPolicy.PLAIN;
        }
        if (toolPolicy == null) {
            toolPolicy = ToolPolicy.DISALLOW;
        }
        if (verify == null) {
            verify = VerifyPolicy.NONE;
        }
        if (budget == null) {
            budget = Budget.DEFAULT;
        }
    }
}
