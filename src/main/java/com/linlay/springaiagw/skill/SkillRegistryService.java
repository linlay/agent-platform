package com.linlay.springaiagw.skill;

import com.linlay.springaiagw.service.RuntimeResourceSyncService;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Stream;

@Service
public class SkillRegistryService {

    private static final Logger log = LoggerFactory.getLogger(SkillRegistryService.class);
    private static final String SKILL_FILE = "SKILL.md";

    private final SkillCatalogProperties properties;
    @SuppressWarnings("unused")
    private final RuntimeResourceSyncService runtimeResourceSyncService;

    private final Object reloadLock = new Object();
    private volatile Map<String, SkillDescriptor> byId = Map.of();

    public SkillRegistryService(
            SkillCatalogProperties properties,
            RuntimeResourceSyncService runtimeResourceSyncService
    ) {
        this.properties = properties;
        this.runtimeResourceSyncService = runtimeResourceSyncService;
        refreshSkills();
    }

    public List<SkillDescriptor> list() {
        return byId.values().stream()
                .sorted(Comparator.comparing(SkillDescriptor::id))
                .toList();
    }

    public Optional<SkillDescriptor> find(String skillId) {
        String normalized = normalizeSkillId(skillId);
        if (!StringUtils.hasText(normalized)) {
            return Optional.empty();
        }
        return Optional.ofNullable(byId.get(normalized));
    }

    public void refreshSkills() {
        synchronized (reloadLock) {
            Map<String, SkillDescriptor> loaded = new LinkedHashMap<>();
            Path dir = Path.of(properties.getExternalDir()).toAbsolutePath().normalize();
            if (!Files.exists(dir)) {
                byId = Map.of();
                return;
            }
            if (!Files.isDirectory(dir)) {
                log.warn("Configured skills directory is not a directory: {}", dir);
                byId = Map.of();
                return;
            }

            try (Stream<Path> stream = Files.list(dir)) {
                stream.sorted(Comparator.comparing(path -> path.getFileName().toString()))
                        .forEach(path -> {
                            if (Files.isRegularFile(path)) {
                                log.warn(
                                        "Invalid skill layout entry '{}'. Skill files must be placed at skills/<skill-id>/{}",
                                        path,
                                        SKILL_FILE
                                );
                                return;
                            }
                            if (!Files.isDirectory(path)) {
                                return;
                            }
                            loadSkill(path).ifPresent(descriptor -> {
                                SkillDescriptor old = loaded.putIfAbsent(descriptor.id(), descriptor);
                                if (old != null) {
                                    log.warn(
                                            "Duplicate skill id '{}' found in {} and {}, keep the first one",
                                            descriptor.id(),
                                            old.sourceFile(),
                                            descriptor.sourceFile()
                                    );
                                }
                            });
                        });
            } catch (IOException ex) {
                log.warn("Cannot list skills from {}", dir, ex);
            }

            byId = Map.copyOf(loaded);
            log.debug("Refreshed skill registry, size={}", loaded.size());
        }
    }

    private Optional<SkillDescriptor> loadSkill(Path skillDir) {
        String id = normalizeSkillId(skillDir.getFileName() == null ? "" : skillDir.getFileName().toString());
        if (!StringUtils.hasText(id)) {
            return Optional.empty();
        }

        Path skillFile = skillDir.resolve(SKILL_FILE).normalize();
        if (!skillFile.startsWith(skillDir) || !Files.isRegularFile(skillFile)) {
            log.debug("Skip skill '{}' without {}", id, SKILL_FILE);
            return Optional.empty();
        }

        try {
            ParsedSkill parsed = parseSkillMarkdown(Files.readString(skillFile));
            String name = StringUtils.hasText(parsed.name) ? parsed.name : id;
            String description = StringUtils.hasText(parsed.description) ? parsed.description : "";
            PromptSlice promptSlice = slicePrompt(parsed.body);
            return Optional.of(new SkillDescriptor(
                    id,
                    name,
                    description,
                    promptSlice.prompt,
                    skillFile.toString(),
                    promptSlice.truncated
            ));
        } catch (Exception ex) {
            log.warn("Skip invalid skill file: {}", skillFile, ex);
            return Optional.empty();
        }
    }

    private PromptSlice slicePrompt(String rawPrompt) {
        String prompt = rawPrompt == null ? "" : rawPrompt.trim();
        int maxPromptChars = Math.max(0, properties.getMaxPromptChars());
        if (maxPromptChars == 0 || prompt.length() <= maxPromptChars) {
            return new PromptSlice(prompt, false);
        }
        return new PromptSlice(
                prompt.substring(0, maxPromptChars)
                        + "\n\n[TRUNCATED: skill prompt exceeds maxPromptChars=" + maxPromptChars + "]",
                true
        );
    }

    private ParsedSkill parseSkillMarkdown(String raw) {
        String normalized = raw == null ? "" : raw.replace("\r\n", "\n");
        List<String> lines = normalized.lines().toList();
        if (lines.isEmpty() || !"---".equals(lines.getFirst().trim())) {
            return new ParsedSkill(null, null, normalized.trim());
        }

        int endIdx = -1;
        for (int i = 1; i < lines.size(); i++) {
            if ("---".equals(lines.get(i).trim())) {
                endIdx = i;
                break;
            }
        }
        if (endIdx < 0) {
            return new ParsedSkill(null, null, normalized.trim());
        }

        String name = null;
        String description = null;
        for (int i = 1; i < endIdx; i++) {
            String line = lines.get(i).trim();
            if (line.isBlank() || line.startsWith("#")) {
                continue;
            }
            int sep = line.indexOf(':');
            if (sep <= 0) {
                continue;
            }
            String key = line.substring(0, sep).trim().toLowerCase(Locale.ROOT);
            String value = unquote(line.substring(sep + 1).trim());
            if ("name".equals(key)) {
                name = value;
            } else if ("description".equals(key)) {
                description = value;
            }
        }

        List<String> bodyLines = new ArrayList<>();
        for (int i = endIdx + 1; i < lines.size(); i++) {
            bodyLines.add(lines.get(i));
        }
        String body = String.join("\n", bodyLines).trim();
        return new ParsedSkill(name, description, body);
    }

    private String normalizeSkillId(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "";
        }
        return raw.trim().toLowerCase(Locale.ROOT);
    }

    private String unquote(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "";
        }
        String trimmed = raw.trim();
        if (trimmed.length() >= 2) {
            if ((trimmed.startsWith("\"") && trimmed.endsWith("\""))
                    || (trimmed.startsWith("'") && trimmed.endsWith("'"))) {
                return trimmed.substring(1, trimmed.length() - 1).trim();
            }
        }
        return trimmed;
    }

    private record ParsedSkill(String name, String description, String body) {
    }

    private record PromptSlice(String prompt, boolean truncated) {
    }
}
