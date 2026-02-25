package com.linlay.agentplatform.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.assertj.core.api.Assertions.assertThat;

class SystemBashAdvancedTest {

    @Test
    void shouldSupportPipelineWhenShellFeaturesEnabled(@TempDir Path tempDir) throws IOException {
        Files.writeString(tempDir.resolve("a.txt"), "foo\nbar\n");
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("cat", "rg"),
                Set.of("cat"),
                true,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "cat a.txt | rg foo"));

        assertThat(result.asText()).contains("exitCode: 0");
        assertThat(result.asText()).contains("mode: shell");
        assertThat(result.asText()).contains("foo");
    }

    @Test
    void shouldSupportHereDocWhenShellFeaturesEnabled(@TempDir Path tempDir) {
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("cat"),
                Set.of("cat"),
                true,
                "bash",
                10_000,
                16_000
        );

        String command = """
                cat <<'EOF' > out.txt
                hello-heredoc
                EOF
                cat out.txt
                """;

        JsonNode result = bash.invoke(Map.of("command", command));

        assertThat(result.asText()).contains("exitCode: 0");
        assertThat(result.asText()).contains("mode: shell");
        assertThat(result.asText()).contains("hello-heredoc");
    }

    @Test
    void shouldSupportLogicalOperatorsWhenShellFeaturesEnabled(@TempDir Path tempDir) {
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("cat", "echo"),
                Set.of("cat"),
                true,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "cat missing.txt || echo fallback"));

        assertThat(result.asText()).contains("exitCode: 0");
        assertThat(result.asText()).contains("fallback");
    }

    @Test
    void shouldRejectDisallowedCommandInsideCommandSubstitution(@TempDir Path tempDir) {
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("echo"),
                Set.of("echo"),
                true,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "echo $(uname -s)"));

        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("Command not allowed: uname");
    }

    @Test
    void shouldRejectRedirectOutsideAllowedPaths(@TempDir Path tempDir) {
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("echo"),
                Set.of("echo"),
                true,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "echo hello > /tmp/outside_bash_tool_test.txt"));

        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("Path not allowed outside authorized directories");
    }

    @Test
    void shouldRejectUnsupportedSourceSyntax(@TempDir Path tempDir) {
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("echo", "source"),
                Set.of("echo"),
                true,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "source script.sh || echo fallback"));

        assertThat(result.asText()).contains("exitCode: -1");
        assertThat(result.asText()).contains("Unsupported syntax for _bash_: source");
    }

    @Test
    void shouldKeepStrictModeWhenShellFeaturesDisabled(@TempDir Path tempDir) throws IOException {
        Files.writeString(tempDir.resolve("a.txt"), "foo\nbar\n");
        SystemBash bash = new SystemBash(
                tempDir,
                List.of(tempDir),
                Set.of("cat", "rg"),
                Set.of("cat"),
                false,
                "bash",
                10_000,
                16_000
        );

        JsonNode result = bash.invoke(Map.of("command", "cat a.txt | rg foo"));

        assertThat(result.asText()).contains("exitCode: 1");
        assertThat(result.asText()).contains("No such file or directory");
        assertThat(result.asText()).contains("mode: strict");
    }
}
