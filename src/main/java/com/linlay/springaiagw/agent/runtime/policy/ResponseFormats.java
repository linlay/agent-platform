package com.linlay.springaiagw.agent.runtime.policy;

public final class ResponseFormats {

    private ResponseFormats() {
    }

    public static final ResponseFormat TEXT_ONLY = new ResponseFormat(
            OutputShape.TEXT_ONLY,
            ToolChoice.NONE,
            null,
            ComputePolicy.MEDIUM,
            4096
    );

    public static final ResponseFormat FINAL_WITH_REASONING_SUMMARY = new ResponseFormat(
            OutputShape.TEXT_WITH_REASONING_SUMMARY,
            ToolChoice.NONE,
            null,
            ComputePolicy.MEDIUM,
            4096
    );

    public static final ResponseFormat TOOL_AUTO_OR_FINAL = new ResponseFormat(
            OutputShape.TOOL_CALL_OR_TEXT,
            ToolChoice.AUTO,
            null,
            ComputePolicy.MEDIUM,
            4096
    );

    public static final ResponseFormat TOOL_AUTO_OR_FINAL_WITH_SUMMARY = new ResponseFormat(
            OutputShape.TOOL_CALL_OR_TEXT_WITH_SUMMARY,
            ToolChoice.AUTO,
            null,
            ComputePolicy.MEDIUM,
            4096
    );

    public static final ResponseFormat FORCE_FINAL_TEXT_ONLY = new ResponseFormat(
            OutputShape.FORCE_FINAL,
            ToolChoice.NONE,
            null,
            ComputePolicy.LOW,
            4096
    );
}
