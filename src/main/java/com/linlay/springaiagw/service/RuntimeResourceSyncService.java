package com.linlay.springaiagw.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.AgentCatalogProperties;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
import com.linlay.springaiagw.config.ViewportCatalogProperties;
import jakarta.annotation.PostConstruct;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.core.io.Resource;
import org.springframework.core.io.support.PathMatchingResourcePatternResolver;
import org.springframework.core.io.support.ResourcePatternResolver;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.io.InputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.Locale;

@Component
public class RuntimeResourceSyncService {

    private static final Logger log = LoggerFactory.getLogger(RuntimeResourceSyncService.class);
    private static final String LEGACY_BASH_FILE = "bash.backend";
    private static final String CANONICAL_BASH_FILE = "_bash_.backend";
    private static final ObjectMapper OBJECT_MAPPER = new ObjectMapper();

    private final ResourcePatternResolver resourceResolver;
    private final Path agentsDir;
    private final Path viewportsDir;
    private final Path toolsDir;

    @Autowired
    public RuntimeResourceSyncService(
            AgentCatalogProperties agentCatalogProperties,
            ViewportCatalogProperties viewportCatalogProperties,
            CapabilityCatalogProperties capabilityCatalogProperties
    ) {
        this(
                new PathMatchingResourcePatternResolver(),
                Path.of(agentCatalogProperties.getExternalDir()).toAbsolutePath().normalize(),
                Path.of(viewportCatalogProperties.getExternalDir()).toAbsolutePath().normalize(),
                Path.of(capabilityCatalogProperties.getToolsExternalDir()).toAbsolutePath().normalize()
        );
    }

    RuntimeResourceSyncService(
            ResourcePatternResolver resourceResolver,
            Path agentsDir,
            Path viewportsDir,
            Path toolsDir
    ) {
        this.resourceResolver = resourceResolver;
        this.agentsDir = agentsDir;
        this.viewportsDir = viewportsDir;
        this.toolsDir = toolsDir;
    }

    @PostConstruct
    public void syncRuntimeDirectories() {
        syncResourceDirectory("agents", agentsDir);
        syncResourceDirectory("viewports", viewportsDir);
        syncResourceDirectory("tools", toolsDir);
        cleanupLegacyToolAliases(toolsDir);
    }

    private void syncResourceDirectory(String resourceDir, Path targetDir) {
        try {
            Files.createDirectories(targetDir);
        } catch (IOException ex) {
            log.warn("Cannot create runtime directory {}", targetDir, ex);
            return;
        }

        String pattern = "classpath*:/" + resourceDir + "/**";
        Resource[] resources;
        try {
            resources = resourceResolver.getResources(pattern);
        } catch (IOException ex) {
            log.warn("Cannot scan resources with pattern {}", pattern, ex);
            return;
        }

        for (Resource resource : resources) {
            if (resource == null || !resource.exists() || !resource.isReadable()) {
                continue;
            }
            String relativePath = resolveRelativePath(resourceDir, resource);
            if (relativePath == null || relativePath.isBlank() || relativePath.endsWith("/")) {
                continue;
            }

            Path target = targetDir.resolve(relativePath).normalize();
            if (!target.startsWith(targetDir)) {
                log.warn("Skip suspicious resource path {} -> {}", relativePath, target);
                continue;
            }

            try (InputStream inputStream = resource.getInputStream()) {
                if (target.getParent() != null) {
                    Files.createDirectories(target.getParent());
                }
                Files.copy(inputStream, target, StandardCopyOption.REPLACE_EXISTING);
                log.debug("Synced runtime resource {} -> {}", resource, target);
            } catch (IOException ex) {
                log.warn("Failed to sync runtime resource {} -> {}", resource, target, ex);
            }
        }
    }

    private String resolveRelativePath(String resourceDir, Resource resource) {
        try {
            String raw = resource.getURL().toString().replace('\\', '/');
            String marker = "/" + resourceDir.toLowerCase(Locale.ROOT) + "/";
            String lower = raw.toLowerCase(Locale.ROOT);
            int idx = lower.lastIndexOf(marker);
            if (idx < 0) {
                return null;
            }
            return raw.substring(idx + marker.length());
        } catch (IOException ex) {
            return null;
        }
    }

    private void cleanupLegacyToolAliases(Path targetToolsDir) {
        if (targetToolsDir == null || !Files.isDirectory(targetToolsDir)) {
            return;
        }
        Path canonicalBash = targetToolsDir.resolve(CANONICAL_BASH_FILE);
        Path legacyBash = targetToolsDir.resolve(LEGACY_BASH_FILE);
        if (!Files.exists(canonicalBash) || !Files.exists(legacyBash)) {
            return;
        }
        if (!isLegacyBashAliasFile(legacyBash)) {
            return;
        }
        try {
            if (Files.deleteIfExists(legacyBash)) {
                log.info("Removed legacy tool alias file: {}", legacyBash);
            }
        } catch (IOException ex) {
            log.warn("Failed to remove legacy tool alias file: {}", legacyBash, ex);
        }
    }

    private boolean isLegacyBashAliasFile(Path file) {
        JsonNode root;
        try {
            root = OBJECT_MAPPER.readTree(Files.readString(file));
        } catch (Exception ex) {
            log.warn("Cannot parse legacy alias candidate file: {}", file, ex);
            return false;
        }
        JsonNode tools = root.path("tools");
        if (!tools.isArray() || tools.isEmpty()) {
            return false;
        }
        for (JsonNode node : tools) {
            if (!node.isObject()) {
                continue;
            }
            String type = node.path("type").asText("");
            String name = node.path("name").asText("");
            if ("function".equals(type) && "_bash_".equals(name)) {
                return true;
            }
        }
        return false;
    }
}
