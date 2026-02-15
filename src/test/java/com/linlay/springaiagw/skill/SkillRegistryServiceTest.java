package com.linlay.springaiagw.skill;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;

@ExtendWith(OutputCaptureExtension.class)
class SkillRegistryServiceTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldLoadSkillFromDirectoryAndFrontmatter() throws Exception {
        Path skillFile = tempDir.resolve("screenshot").resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, """
                ---
                name: "Screen Skill"
                description: "capture screen"
                ---
                # Steps
                do this
                """);

        SkillCatalogProperties properties = new SkillCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        SkillRegistryService service = new SkillRegistryService(properties, null);

        SkillDescriptor descriptor = service.find("screenshot").orElseThrow();
        assertThat(descriptor.id()).isEqualTo("screenshot");
        assertThat(descriptor.name()).isEqualTo("Screen Skill");
        assertThat(descriptor.description()).isEqualTo("capture screen");
        assertThat(descriptor.prompt()).contains("# Steps");
    }

    @Test
    void shouldTruncatePromptWhenConfiguredMaxExceeded() throws Exception {
        Path skillFile = tempDir.resolve("doc").resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, "name: doc\n".repeat(1000));

        SkillCatalogProperties properties = new SkillCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        properties.setMaxPromptChars(32);
        SkillRegistryService service = new SkillRegistryService(properties, null);

        SkillDescriptor descriptor = service.find("doc").orElseThrow();
        assertThat(descriptor.promptTruncated()).isTrue();
        assertThat(descriptor.prompt()).contains("[TRUNCATED");
    }

    @Test
    void shouldWarnAndIgnoreInvalidRootFiles(CapturedOutput output) throws Exception {
        Files.writeString(tempDir.resolve("SKILL.md"), "invalid");
        Files.writeString(tempDir.resolve("script.py"), "print('invalid')");
        Path skillFile = tempDir.resolve("math_basic").resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, """
                ---
                name: "Math Basic"
                description: "basic math"
                ---
                run script
                """);

        SkillCatalogProperties properties = new SkillCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        SkillRegistryService service = new SkillRegistryService(properties, null);

        assertThat(service.list()).hasSize(1);
        assertThat(service.find("math_basic")).isPresent();
        assertThat(output.getOut() + output.getErr())
                .contains("Invalid skill layout entry")
                .contains("skills/<skill-id>/SKILL.md");
    }

    @Test
    void shouldLoadMultipleSkillDirectories() throws Exception {
        writeSkill("math_basic", "Math Basic", "basic arithmetic");
        writeSkill("math_stats", "Math Stats", "stats operations");
        writeSkill("text_utils", "Text Utils", "text metrics");

        SkillCatalogProperties properties = new SkillCatalogProperties();
        properties.setExternalDir(tempDir.toString());
        SkillRegistryService service = new SkillRegistryService(properties, null);

        assertThat(service.list())
                .extracting(SkillDescriptor::id)
                .containsExactlyInAnyOrder("math_basic", "math_stats", "text_utils");
        assertThat(service.find("math_basic").orElseThrow().name()).isEqualTo("Math Basic");
        assertThat(service.find("math_stats").orElseThrow().name()).isEqualTo("Math Stats");
        assertThat(service.find("text_utils").orElseThrow().name()).isEqualTo("Text Utils");
    }

    private void writeSkill(String id, String name, String description) throws Exception {
        Path skillFile = tempDir.resolve(id).resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, """
                ---
                name: "%s"
                description: "%s"
                ---
                body
                """.formatted(name, description));
    }
}
