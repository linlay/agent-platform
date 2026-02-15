package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class SkillScriptRunToolTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldUseSystemToolName() {
        SkillScriptRunTool tool = new SkillScriptRunTool(tempDir.resolve("skills"));
        assertThat(tool.name()).isEqualTo("_skill_script_run_");
    }

    @Test
    void shouldRunPythonScriptSuccessfully() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello-skill')");

        SkillScriptRunTool tool = new SkillScriptRunTool(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "scripts/echo.py",
                "args", List.of(),
                "timeoutMs", 5000
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("timedOut").asBoolean()).isFalse();
        assertThat(result.path("exitCode").asInt()).isEqualTo(0);
        assertThat(result.path("stdout").asText()).contains("hello-skill");
    }

    @Test
    void shouldRejectPathTraversal() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello')");

        SkillScriptRunTool tool = new SkillScriptRunTool(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "../outside.py"
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("Illegal script path");
    }

    @Test
    void shouldReturnTimeoutWhenScriptRunsTooLong() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("slow.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, """
                import time
                time.sleep(2)
                print("done")
                """);

        SkillScriptRunTool tool = new SkillScriptRunTool(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "scripts/slow.py",
                "timeoutMs", 100
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("timedOut").asBoolean()).isTrue();
        assertThat(result.path("exitCode").asInt()).isEqualTo(-1);
    }

    @Test
    void shouldReturnErrorWhenInterpreterMissing() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello')");

        SkillScriptRunTool tool = new SkillScriptRunTool(skillsRoot, "__missing_python__", "bash");
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "scripts/echo.py"
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("timedOut").asBoolean()).isFalse();
        assertThat(result.path("exitCode").asInt()).isEqualTo(-1);
        assertThat(result.path("stderr").asText()).contains("__missing_python__");
    }
}
