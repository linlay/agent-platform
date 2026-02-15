package com.linlay.springaiagw.agent.runtime.policy;

public record Budget(
        int maxModelCalls,
        int maxToolCalls,
        int maxSteps,
        long timeoutMs
) {
    public static final Budget DEFAULT = new Budget(15, 20, 8, 120_000);
    public static final Budget LIGHT = new Budget(3, 5, 3, 30_000);
    public static final Budget HEAVY = new Budget(30, 50, 15, 300_000);

    public Budget {
        if (maxModelCalls <= 0) {
            maxModelCalls = DEFAULT.maxModelCalls;
        }
        if (maxToolCalls <= 0) {
            maxToolCalls = DEFAULT.maxToolCalls;
        }
        if (maxSteps <= 0) {
            maxSteps = DEFAULT.maxSteps;
        }
        if (timeoutMs <= 0) {
            timeoutMs = DEFAULT.timeoutMs;
        }
    }
}
