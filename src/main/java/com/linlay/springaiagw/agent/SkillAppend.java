package com.linlay.springaiagw.agent;

public record SkillAppend(
        String catalogHeader,
        String disclosureHeader,
        String instructionsLabel
) {
    public static final SkillAppend DEFAULTS = new SkillAppend(
            "可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:",
            "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:",
            "instructions"
    );
}
