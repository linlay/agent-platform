package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.config.ViewportCatalogProperties;
import com.linlay.springaiagw.model.ViewportType;
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
import java.util.Set;
import java.util.stream.Stream;

@Service
public class ViewportRegistryService {

    private static final Logger log = LoggerFactory.getLogger(ViewportRegistryService.class);
    private static final Set<String> SUPPORTED_SUFFIXES = Set.of(
            ".json_schema", ".html", ".qlc", ".dqlc", ".custom"
    );

    private final ObjectMapper objectMapper;
    private final ViewportCatalogProperties properties;
    @SuppressWarnings("unused")
    private final RuntimeResourceSyncService runtimeResourceSyncService;

    private final Object refreshLock = new Object();
    private volatile Map<String, ViewportEntry> byKey = Map.of();

    public ViewportRegistryService(
            ObjectMapper objectMapper,
            ViewportCatalogProperties properties,
            RuntimeResourceSyncService runtimeResourceSyncService
    ) {
        this.objectMapper = objectMapper;
        this.properties = properties;
        this.runtimeResourceSyncService = runtimeResourceSyncService;
        refreshViewports();
    }

    public Optional<ViewportEntry> find(String viewportKey) {
        if (!StringUtils.hasText(viewportKey)) {
            return Optional.empty();
        }
        String normalized = normalizeKey(viewportKey);
        return Optional.ofNullable(byKey.get(normalized));
    }

    public void refreshViewports() {
        synchronized (refreshLock) {
            Path dir = Path.of(properties.getExternalDir()).toAbsolutePath().normalize();
            Map<String, ViewportEntry> updated = new LinkedHashMap<>();

            if (!Files.exists(dir)) {
                byKey = Map.of();
                return;
            }
            if (!Files.isDirectory(dir)) {
                log.warn("Configured viewport external path is not a directory: {}", dir);
                return;
            }

            List<Path> files = new ArrayList<>();
            try (Stream<Path> stream = Files.list(dir)) {
                stream.filter(Files::isRegularFile)
                        .filter(this::isSupported)
                        .sorted(Comparator.comparing(path -> path.getFileName().toString()))
                        .forEach(files::add);
            } catch (IOException ex) {
                log.warn("Cannot list viewport files from {}", dir, ex);
                return;
            }

            for (Path file : files) {
                tryLoad(file).ifPresent(entry -> {
                    String key = entry.viewportKey();
                    if (updated.containsKey(key)) {
                        log.warn("Duplicate viewportKey '{}' detected, skip file {}", key, file);
                        return;
                    }
                    updated.put(key, entry);
                });
            }

            byKey = Map.copyOf(updated);
            log.debug("Refreshed viewport registry, size={}, dir={}", updated.size(), dir);
        }
    }

    private Optional<ViewportEntry> tryLoad(Path file) {
        String fileName = file.getFileName().toString();
        String suffix = resolveSuffix(fileName);
        if (suffix == null) {
            return Optional.empty();
        }
        String baseName = fileName.substring(0, fileName.length() - suffix.length());
        String viewportKey = normalizeKey(baseName);
        if (viewportKey.isBlank()) {
            log.warn("Skip viewport file with empty key: {}", file);
            return Optional.empty();
        }

        ViewportType type = resolveType(suffix);
        if (type == null) {
            return Optional.empty();
        }

        try {
            String raw = Files.readString(file);
            Object payload;
            if (type == ViewportType.HTML) {
                payload = raw;
            } else {
                payload = objectMapper.readValue(raw, Object.class);
            }
            return Optional.of(new ViewportEntry(viewportKey, type, payload));
        } catch (Exception ex) {
            log.warn("Skip invalid viewport file: {}", file, ex);
            return Optional.empty();
        }
    }

    private boolean isSupported(Path file) {
        String fileName = file.getFileName().toString().toLowerCase(Locale.ROOT);
        return resolveSuffix(fileName) != null;
    }

    private String resolveSuffix(String fileName) {
        String lower = fileName.toLowerCase(Locale.ROOT);
        return SUPPORTED_SUFFIXES.stream()
                .sorted((left, right) -> Integer.compare(right.length(), left.length()))
                .filter(lower::endsWith)
                .findFirst()
                .orElse(null);
    }

    private ViewportType resolveType(String suffix) {
        return switch (suffix.toLowerCase(Locale.ROOT)) {
            case ".html" -> ViewportType.HTML;
            case ".qlc" -> ViewportType.QLC;
            default -> null;
        };
    }

    private String normalizeKey(String raw) {
        return raw == null ? "" : raw.trim().toLowerCase(Locale.ROOT);
    }

    public record ViewportEntry(
            String viewportKey,
            ViewportType viewportType,
            Object payload
    ) {
    }
}
