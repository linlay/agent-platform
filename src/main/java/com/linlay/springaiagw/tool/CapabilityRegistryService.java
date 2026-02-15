package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
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
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Stream;

@Service
public class CapabilityRegistryService {

    private static final Logger log = LoggerFactory.getLogger(CapabilityRegistryService.class);
    private static final TypeReference<Map<String, Object>> MAP_TYPE = new TypeReference<>() {
    };
    private static final String BACKEND_SUFFIX = ".backend";
    private static final String ACTION_SUFFIX = ".action";
    private static final Set<String> FRONTEND_SUFFIXES = Set.of(".frontend");

    private final ObjectMapper objectMapper;
    private final CapabilityCatalogProperties properties;
    @SuppressWarnings("unused")
    private final RuntimeResourceSyncService runtimeResourceSyncService;

    private final Object reloadLock = new Object();
    private volatile Map<String, CapabilityDescriptor> byName = Map.of();

    public CapabilityRegistryService(
            ObjectMapper objectMapper,
            CapabilityCatalogProperties properties,
            RuntimeResourceSyncService runtimeResourceSyncService
    ) {
        this.objectMapper = objectMapper;
        this.properties = properties;
        this.runtimeResourceSyncService = runtimeResourceSyncService;
        refreshCapabilities();
    }

    public List<CapabilityDescriptor> list() {
        return byName.values().stream()
                .sorted(Comparator.comparing(CapabilityDescriptor::name))
                .toList();
    }

    public Optional<CapabilityDescriptor> find(String toolName) {
        if (!StringUtils.hasText(toolName)) {
            return Optional.empty();
        }
        return Optional.ofNullable(byName.get(normalizeName(toolName)));
    }

    public void refreshCapabilities() {
        synchronized (reloadLock) {
            Map<String, CapabilityDescriptor> loaded = new LinkedHashMap<>();
            Set<String> conflicts = new HashSet<>();

            loadToolsDirectory(Path.of(properties.getToolsExternalDir()).toAbsolutePath().normalize(), loaded, conflicts);

            byName = Map.copyOf(loaded);
            log.debug("Refreshed capability registry, size={}", loaded.size());
        }
    }

    private void loadToolsDirectory(Path dir, Map<String, CapabilityDescriptor> loaded, Set<String> conflicts) {
        if (!Files.exists(dir)) {
            return;
        }
        if (!Files.isDirectory(dir)) {
            log.warn("Configured tools directory is not a directory: {}", dir);
            return;
        }
        for (Path file : sortedFiles(dir)) {
            String fileName = file.getFileName().toString();
            String lower = fileName.toLowerCase(Locale.ROOT);
            if (lower.endsWith(BACKEND_SUFFIX)) {
                parseFile(file, CapabilityKind.BACKEND, "function", null, loaded, conflicts);
                continue;
            }
            if (lower.endsWith(ACTION_SUFFIX)) {
                parseFile(file, CapabilityKind.ACTION, "action", null, loaded, conflicts);
                continue;
            }
            String frontendSuffix = resolveFrontendSuffix(lower);
            if (frontendSuffix == null) {
                continue;
            }
            String viewportKey = fileName.substring(0, fileName.length() - frontendSuffix.length()).toLowerCase(Locale.ROOT);
            parseFile(file, CapabilityKind.FRONTEND, frontendSuffix.substring(1), viewportKey, loaded, conflicts);
        }
    }

    private void parseFile(
            Path file,
            CapabilityKind kind,
            String toolType,
            String viewportKey,
            Map<String, CapabilityDescriptor> loaded,
            Set<String> conflicts
    ) {
        JsonNode root;
        try {
            root = objectMapper.readTree(Files.readString(file));
        } catch (Exception ex) {
            log.warn("Skip invalid capability file: {}", file, ex);
            return;
        }

        JsonNode tools = root.path("tools");
        if (!tools.isArray()) {
            log.warn("Skip capability file without tools[]: {}", file);
            return;
        }

        for (JsonNode node : tools) {
            if (node == null || !node.isObject()) {
                continue;
            }
            String type = normalize(node.path("type").asText(""));
            if (!"function".equals(type)) {
                continue;
            }
            String name = normalizeName(node.path("name").asText(""));
            if (name.isBlank()) {
                continue;
            }
            if (conflicts.contains(name)) {
                continue;
            }

            Map<String, Object> parameters = parseParameters(node.get("parameters"));
            String description = normalize(node.path("description").asText(""));
            String prompt = normalize(node.path("prompt").asText(""));
            Boolean strict = node.has("strict") ? node.path("strict").asBoolean(false) : null;
            String toolApi = node.has("toolApi") && node.get("toolApi").isTextual()
                    ? node.get("toolApi").asText()
                    : null;

            CapabilityDescriptor descriptor = new CapabilityDescriptor(
                    name,
                    description,
                    prompt,
                    parameters,
                    strict,
                    kind,
                    toolType,
                    toolApi,
                    viewportKey,
                    file.toString()
            );

            CapabilityDescriptor old = loaded.putIfAbsent(name, descriptor);
            if (old != null) {
                loaded.remove(name);
                conflicts.add(name);
                log.warn("Duplicate capability name '{}' found in {} and {}, both skipped", name, old.sourceFile(), file);
            }
        }
    }

    private List<Path> sortedFiles(Path dir) {
        List<Path> files = new ArrayList<>();
        try (Stream<Path> stream = Files.list(dir)) {
            stream.filter(Files::isRegularFile)
                    .sorted(Comparator.comparing(path -> path.getFileName().toString()))
                    .forEach(files::add);
        } catch (IOException ex) {
            log.warn("Cannot list capability files from {}", dir, ex);
        }
        return files;
    }

    private String resolveFrontendSuffix(String fileName) {
        return FRONTEND_SUFFIXES.stream()
                .filter(fileName::endsWith)
                .findFirst()
                .orElse(null);
    }

    private Map<String, Object> parseParameters(JsonNode node) {
        if (node == null || node.isNull()) {
            return defaultParameters();
        }
        if (!node.isObject()) {
            return defaultParameters();
        }
        try {
            Map<String, Object> converted = objectMapper.convertValue(node, MAP_TYPE);
            return converted == null || converted.isEmpty() ? defaultParameters() : converted;
        } catch (IllegalArgumentException ex) {
            return defaultParameters();
        }
    }

    private Map<String, Object> defaultParameters() {
        return Map.of(
                "type", "object",
                "properties", Map.of(),
                "additionalProperties", true
        );
    }

    private String normalizeName(String raw) {
        return normalize(raw).toLowerCase(Locale.ROOT);
    }

    private String normalize(String value) {
        return value == null ? "" : value.trim();
    }
}
