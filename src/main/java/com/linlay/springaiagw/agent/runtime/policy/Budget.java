package com.linlay.springaiagw.agent.runtime.policy;

public record Budget(
        int maxModelCalls,
        int maxToolCalls,
        long timeoutMs,
        int retryCount
) {
    public static final Budget DEFAULT = new Budget(15, 20, 120_000, 0);
    public static final Budget LIGHT = new Budget(3, 5, 30_000, 0);
    public static final Budget HEAVY = new Budget(30, 50, 300_000, 0);

    public Budget {
        if (maxModelCalls <= 0) {
            maxModelCalls = DEFAULT.maxModelCalls;
        }
        if (maxToolCalls <= 0) {
            maxToolCalls = DEFAULT.maxToolCalls;
        }
        if (timeoutMs <= 0) {
            timeoutMs = DEFAULT.timeoutMs;
        }
        if (retryCount < 0) {
            retryCount = 0;
        }
    }
}
