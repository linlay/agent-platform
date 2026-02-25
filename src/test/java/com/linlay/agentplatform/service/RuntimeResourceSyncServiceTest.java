package com.linlay.agentplatform.service;

import static org.assertj.core.api.Assertions.assertThat;

import java.nio.file.Files;
import java.nio.file.Path;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.core.io.support.PathMatchingResourcePatternResolver;

import com.linlay.agentplatform.agent.AgentCatalogProperties;
import com.linlay.agentplatform.config.CapabilityCatalogProperties;
import com.linlay.agentplatform.config.ViewportCatalogProperties;
import com.linlay.agentplatform.model.ModelCatalogProperties;
import com.linlay.agentplatform.skill.SkillCatalogProperties;

class RuntimeResourceSyncServiceTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldOverwriteBuiltInResourcesAndKeepExtraFiles() throws Exception {
        Path agentsDir = tempDir.resolve("agents");
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Path skillsDir = tempDir.resolve("skills");
        Path modelsDir = tempDir.resolve("models");
        Files.createDirectories(agentsDir);
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);
        Files.createDirectories(skillsDir);
        Files.createDirectories(modelsDir);

        Path modePlainAgent = agentsDir.resolve("demoModePlain.json");
        Path weatherTool = toolsDir.resolve("mock_city_weather.backend");
        Path skillScriptTool = toolsDir.resolve("_skill_run_script_.backend");
        Path weatherViewport = viewportsDir.resolve("show_weather_card.html");
        Path extraAgent = agentsDir.resolve("custom_agent.json");
        Path extraTool = toolsDir.resolve("custom.backend");
        Path extraViewport = viewportsDir.resolve("custom.html");
        Path qwenModel = modelsDir.resolve("bailian-qwen3-max.json");
        Path extraModel = modelsDir.resolve("custom-model.json");
        Path screenshotSkillFile = skillsDir.resolve("screenshot").resolve("SKILL.md");
        Path extraSkillFile = skillsDir.resolve("custom_skill").resolve("SKILL.md");

        Files.writeString(modePlainAgent, "old-agent-content");
        Files.writeString(weatherTool, "old-tool-content");
        Files.writeString(weatherViewport, "old-viewport-content");
        Files.writeString(extraAgent, "custom agent content");
        Files.writeString(extraTool, "custom tool content");
        Files.writeString(extraViewport, "custom viewport content");
        Files.writeString(qwenModel, "old-model-content");
        Files.writeString(extraModel, "custom model content");
        Files.createDirectories(screenshotSkillFile.getParent());
        Files.createDirectories(extraSkillFile.getParent());
        Files.writeString(screenshotSkillFile, "old-skill-content");
        Files.writeString(extraSkillFile, "custom skill content");

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                agentsDir,
                viewportsDir,
                toolsDir,
                skillsDir,
                modelsDir
        );
        service.syncRuntimeDirectories();
        service.syncRuntimeDirectories();

        String syncedTool = Files.readString(weatherTool);
        String syncedSkillScriptTool = Files.readString(skillScriptTool);
        String syncedViewport = Files.readString(weatherViewport);
        String syncedAgent = Files.readString(modePlainAgent);
        String syncedSkill = Files.readString(screenshotSkillFile);
        String syncedModel = Files.readString(qwenModel);

        assertThat(syncedAgent).contains("\"mode\"").contains("\"ONESHOT\"");
        assertThat(syncedTool).contains("\"name\": \"mock_city_weather\"");
        assertThat(syncedTool).contains("\"afterCallHint\"");
        assertThat(syncedSkillScriptTool).contains("\"name\": \"_skill_run_script_\"");
        assertThat(syncedViewport).contains("<title>天气卡片</title>");
        assertThat(syncedSkill).contains("name: \"screenshot\"");
        assertThat(syncedModel).contains("\"key\": \"bailian-qwen3-max\"");
        assertThat(skillsDir.resolve("math_basic").resolve("SKILL.md")).exists();
        assertThat(skillsDir.resolve("math_stats").resolve("SKILL.md")).exists();
        assertThat(skillsDir.resolve("text_utils").resolve("SKILL.md")).exists();
        assertThat(Files.readString(extraAgent)).isEqualTo("custom agent content");
        assertThat(Files.readString(extraTool)).isEqualTo("custom tool content");
        assertThat(Files.readString(extraViewport)).isEqualTo("custom viewport content");
        assertThat(Files.readString(extraSkillFile)).isEqualTo("custom skill content");
        assertThat(Files.readString(extraModel)).isEqualTo("custom model content");
    }

    @Test
    void shouldSyncToConfiguredDirectoriesInsteadOfUserDirRoot() throws Exception {
        Path configuredAgentsDir = tempDir.resolve("configured").resolve("agents");
        Path configuredViewportsDir = tempDir.resolve("configured").resolve("viewports");
        Path configuredToolsDir = tempDir.resolve("configured").resolve("tools");
        Path configuredSkillsDir = tempDir.resolve("configured").resolve("skills");
        Path configuredModelsDir = tempDir.resolve("configured").resolve("models");
        Path legacyUserDir = tempDir.resolve("legacy-user-dir");

        AgentCatalogProperties agentProperties = new AgentCatalogProperties();
        agentProperties.setExternalDir(configuredAgentsDir.toString());
        ViewportCatalogProperties viewportProperties = new ViewportCatalogProperties();
        viewportProperties.setExternalDir(configuredViewportsDir.toString());
        CapabilityCatalogProperties capabilityProperties = new CapabilityCatalogProperties();
        capabilityProperties.setToolsExternalDir(configuredToolsDir.toString());
        SkillCatalogProperties skillProperties = new SkillCatalogProperties();
        skillProperties.setExternalDir(configuredSkillsDir.toString());
        ModelCatalogProperties modelProperties = new ModelCatalogProperties();
        modelProperties.setExternalDir(configuredModelsDir.toString());

        String originalUserDir = System.getProperty("user.dir");
        try {
            System.setProperty("user.dir", legacyUserDir.toString());
            RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                    agentProperties,
                    viewportProperties,
                    capabilityProperties,
                    skillProperties,
                    modelProperties
            );
            service.syncRuntimeDirectories();
        } finally {
            if (originalUserDir == null) {
                System.clearProperty("user.dir");
            } else {
                System.setProperty("user.dir", originalUserDir);
            }
        }

        assertThat(configuredAgentsDir.resolve("demoModePlain.json")).exists();
        assertThat(configuredViewportsDir.resolve("show_weather_card.html")).exists();
        assertThat(configuredToolsDir.resolve("mock_city_weather.backend")).exists();
        assertThat(configuredSkillsDir.resolve("screenshot").resolve("SKILL.md")).exists();
        assertThat(configuredModelsDir.resolve("bailian-qwen3-max.json")).exists();
        assertThat(configuredSkillsDir.resolve("math_basic").resolve("SKILL.md")).exists();
        assertThat(configuredSkillsDir.resolve("math_stats").resolve("SKILL.md")).exists();
        assertThat(configuredSkillsDir.resolve("text_utils").resolve("SKILL.md")).exists();
        assertThat(legacyUserDir.resolve("agents")).doesNotExist();
        assertThat(legacyUserDir.resolve("viewports")).doesNotExist();
        assertThat(legacyUserDir.resolve("tools")).doesNotExist();
        assertThat(legacyUserDir.resolve("skills")).doesNotExist();
        assertThat(legacyUserDir.resolve("models")).doesNotExist();
    }

    @Test
    void shouldRemoveLegacyBashAliasWhenCanonicalToolExists() throws Exception {
        Path agentsDir = tempDir.resolve("agents");
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Files.createDirectories(agentsDir);
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);

        Path legacyBash = toolsDir.resolve("bash.backend");
        Files.writeString(legacyBash, """
                {
                  "tools": [
                    {
                      "type": "function",
                      "name": "_bash_",
                      "description": "legacy alias"
                    }
                  ]
                }
                """);

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                agentsDir,
                viewportsDir,
                toolsDir,
                tempDir.resolve("skills"),
                tempDir.resolve("models")
        );
        service.syncRuntimeDirectories();

        assertThat(toolsDir.resolve("_bash_.backend")).exists();
        assertThat(legacyBash).doesNotExist();
    }

    @Test
    void shouldRemoveLegacySkillScriptAliasWhenCanonicalToolExists() throws Exception {
        Path agentsDir = tempDir.resolve("agents");
        Path toolsDir = tempDir.resolve("tools");
        Path viewportsDir = tempDir.resolve("viewports");
        Files.createDirectories(agentsDir);
        Files.createDirectories(toolsDir);
        Files.createDirectories(viewportsDir);

        Path legacySkillScript = toolsDir.resolve("skill_script_run.backend");
        Files.writeString(legacySkillScript, """
                {
                  "tools": [
                    {
                      "type": "function",
                      "name": "skill_script_run",
                      "description": "legacy alias"
                    }
                  ]
                }
                """);

        RuntimeResourceSyncService service = new RuntimeResourceSyncService(
                new PathMatchingResourcePatternResolver(),
                agentsDir,
                viewportsDir,
                toolsDir,
                tempDir.resolve("skills"),
                tempDir.resolve("models")
        );
        service.syncRuntimeDirectories();

        Path canonicalSkillScript = toolsDir.resolve("_skill_run_script_.backend");
        assertThat(canonicalSkillScript).exists();
        assertThat(Files.readString(canonicalSkillScript)).contains("\"name\": \"_skill_run_script_\"");
        assertThat(legacySkillScript).doesNotExist();
    }
}
