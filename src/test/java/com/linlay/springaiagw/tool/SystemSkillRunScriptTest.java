package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class SystemSkillRunScriptTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldUseSystemToolName() {
        SystemSkillRunScript tool = new SystemSkillRunScript(tempDir.resolve("skills"));
        assertThat(tool.name()).isEqualTo("_skill_run_script_");
    }

    @Test
    void shouldRunPythonScriptSuccessfully() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello-skill')");

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "scripts/echo.py",
                "args", List.of(),
                "timeoutMs", 5000
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("timedOut").asBoolean()).isFalse();
        assertThat(result.path("exitCode").asInt()).isEqualTo(0);
        assertThat(result.path("scriptSource").asText()).isEqualTo("file");
        assertThat(result.path("stdout").asText()).contains("hello-skill");
    }

    @Test
    void shouldRunInlinePythonCodeSuccessfullyAndCleanupTempFile() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path skillDir = skillsRoot.resolve("screenshot");
        Files.createDirectories(skillDir);

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "pythonCode", "print('hello-inline')",
                "args", List.of()
        ));

        assertThat(result.path("ok").asBoolean()).isTrue();
        assertThat(result.path("timedOut").asBoolean()).isFalse();
        assertThat(result.path("exitCode").asInt()).isEqualTo(0);
        assertThat(result.path("scriptSource").asText()).isEqualTo("inline");
        assertThat(result.path("stdout").asText()).contains("hello-inline");

        Path inlineScriptPath = Path.of(result.path("script").asText());
        assertThat(inlineScriptPath).doesNotExist();
    }

    @Test
    void shouldRejectWhenScriptAndPythonCodeProvided() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello')");

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "script", "scripts/echo.py",
                "pythonCode", "print('hello-inline')"
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("mutually exclusive");
    }

    @Test
    void shouldRequireScriptOrPythonCode() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Files.createDirectories(skillsRoot.resolve("screenshot"));

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of("skill", "screenshot"));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("Missing argument: script or pythonCode");
    }

    @Test
    void shouldRejectTooLongInlinePythonCode() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Files.createDirectories(skillsRoot.resolve("screenshot"));
        String longCode = "a".repeat(70_000);

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "pythonCode", longCode
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("error").asText()).contains("pythonCode too long");
    }

    @Test
    void shouldRejectPathTraversal() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello')");

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
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

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
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
    void shouldReturnTimeoutWhenInlineScriptRunsTooLongAndCleanup() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Files.createDirectories(skillsRoot.resolve("screenshot"));

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot);
        JsonNode result = tool.invoke(Map.of(
                "skill", "screenshot",
                "pythonCode", """
                        import time
                        time.sleep(2)
                        print("done")
                        """,
                "timeoutMs", 100
        ));

        assertThat(result.path("ok").asBoolean()).isFalse();
        assertThat(result.path("timedOut").asBoolean()).isTrue();
        assertThat(result.path("exitCode").asInt()).isEqualTo(-1);
        assertThat(result.path("scriptSource").asText()).isEqualTo("inline");

        Path inlineScriptPath = Path.of(result.path("script").asText());
        assertThat(inlineScriptPath).doesNotExist();
    }

    @Test
    void shouldReturnErrorWhenInterpreterMissing() throws Exception {
        Path skillsRoot = tempDir.resolve("skills");
        Path script = skillsRoot.resolve("screenshot").resolve("scripts").resolve("echo.py");
        Files.createDirectories(script.getParent());
        Files.writeString(script, "print('hello')");

        SystemSkillRunScript tool = new SystemSkillRunScript(skillsRoot, "__missing_python__", "bash");
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
