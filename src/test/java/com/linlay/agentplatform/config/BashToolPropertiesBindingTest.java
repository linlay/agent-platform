package com.linlay.agentplatform.config;

import com.fasterxml.jackson.databind.JsonNode;
import com.linlay.agentplatform.tool.SystemBash;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class BashToolPropertiesBindingTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(BashToolConfiguration.class);

    @Test
    void indexedPropertiesShouldBindAndAllowConfiguredPaths(@TempDir Path tempDir) throws IOException {
        Path file = tempDir.resolve("demo.txt");
        Files.writeString(file, "hello-indexed");

        contextRunner
                .withPropertyValues(
                        "agent.tools.bash.working-directory=" + tempDir,
                        "agent.tools.bash.allowed-paths[0]=" + tempDir,
                        "agent.tools.bash.allowed-commands[0]=cat"
                )
                .run(context -> {
                    BashToolProperties properties = context.getBean(BashToolProperties.class);
                    assertThat(properties.getAllowedCommands()).containsExactly("cat");
                    assertThat(properties.getAllowedPaths()).containsExactly(tempDir.toString());

                    SystemBash bash = context.getBean(SystemBash.class);
                    JsonNode result = bash.invoke(Map.of("command", "cat demo.txt"));
                    assertThat(result.asText()).contains("exitCode: 0");
                    assertThat(result.asText()).contains("hello-indexed");
                });
    }

    @Test
    void commaSeparatedPropertiesShouldBindAndAllowConfiguredPaths(@TempDir Path tempDir) throws IOException {
        Path workingDir = tempDir.resolve("workspace");
        Path externalDir = tempDir.resolve("external");
        Files.createDirectories(workingDir);
        Files.createDirectories(externalDir);
        Path externalFile = externalDir.resolve("demo.txt");
        Files.writeString(externalFile, "hello-comma");

        contextRunner
                .withPropertyValues(
                        "agent.tools.bash.working-directory=" + workingDir,
                        "agent.tools.bash.allowed-paths=" + workingDir + "," + externalDir,
                        "agent.tools.bash.allowed-commands=cat,echo",
                        "agent.tools.bash.path-checked-commands=cat,git"
                )
                .run(context -> {
                    BashToolProperties properties = context.getBean(BashToolProperties.class);
                    assertThat(properties.getAllowedCommands()).contains("cat", "echo");
                    assertThat(properties.getAllowedPaths()).contains(workingDir.toString(), externalDir.toString());

                    SystemBash bash = context.getBean(SystemBash.class);
                    JsonNode catResult = bash.invoke(Map.of("command", "cat " + externalFile));
                    JsonNode echoResult = bash.invoke(Map.of("command", "echo ok"));

                    assertThat(catResult.asText()).contains("exitCode: 0");
                    assertThat(catResult.asText()).contains("hello-comma");
                    assertThat(echoResult.asText()).contains("exitCode: 0");
                    assertThat(echoResult.asText()).contains("ok");
                });
    }

    @Configuration(proxyBeanMethods = false)
    @EnableConfigurationProperties(BashToolProperties.class)
    static class BashToolConfiguration {

        @Bean
        SystemBash systemBash(BashToolProperties properties) {
            return new SystemBash(properties);
        }
    }
}
