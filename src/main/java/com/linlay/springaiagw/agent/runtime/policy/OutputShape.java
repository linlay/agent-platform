package com.linlay.springaiagw.agent.runtime.policy;

public enum OutputShape {
    TEXT_ONLY,
    TEXT_WITH_REASONING_SUMMARY,
    TOOL_CALL_OR_TEXT,
    TOOL_CALL_OR_TEXT_WITH_SUMMARY,
    JSON_SCHEMA,
    FORCE_FINAL
}
