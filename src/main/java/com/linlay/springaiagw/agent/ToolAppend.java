package com.linlay.springaiagw.agent;

public record ToolAppend(
        String toolDescriptionTitle,
        String afterCallHintTitle
) {
    public static final ToolAppend DEFAULTS = new ToolAppend(
            "工具说明:",
            "工具调用后推荐指令:"
    );
}
