package com.linlay.springaiagw.skill;

public record SkillDescriptor(
        String id,
        String name,
        String description,
        String prompt,
        String sourceFile,
        boolean promptTruncated
) {
    public SkillDescriptor {
        id = normalize(id);
        name = normalize(name);
        description = normalize(description);
        prompt = prompt == null ? "" : prompt;
        sourceFile = sourceFile == null ? "" : sourceFile.trim();
    }

    private static String normalize(String value) {
        return value == null ? "" : value.trim();
    }
}
