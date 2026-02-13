package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Function;
import java.util.stream.Collectors;

@Component
public class ToolRegistry {

    private static final Logger log = LoggerFactory.getLogger(ToolRegistry.class);

    private final Map<String, BaseTool> nativeToolsByName;
    private final CapabilityRegistryService capabilityRegistryService;
    private final Set<String> missingBackendWarnings = ConcurrentHashMap.newKeySet();

    public ToolRegistry(List<BaseTool> tools) {
        this.nativeToolsByName = buildNativeToolsByName(tools);
        this.capabilityRegistryService = null;
    }

    @Autowired
    public ToolRegistry(List<BaseTool> tools, ObjectProvider<CapabilityRegistryService> capabilityRegistryServiceProvider) {
        this.nativeToolsByName = buildNativeToolsByName(tools);
        this.capabilityRegistryService = capabilityRegistryServiceProvider.getIfAvailable();
    }

    public JsonNode invoke(String toolName, Map<String, Object> args) {
        BaseTool tool = nativeToolsByName.get(normalizeName(toolName));
        if (Objects.isNull(tool)) {
            throw new IllegalArgumentException("Unknown tool: " + toolName);
        }
        return tool.invoke(args);
    }

    public List<BaseTool> list() {
        Map<String, BaseTool> merged = new LinkedHashMap<>();
        nativeToolsByName.entrySet().stream()
                .sorted(Map.Entry.comparingByKey())
                .forEach(entry -> merged.put(entry.getKey(), entry.getValue()));

        if (capabilityRegistryService == null) {
            return List.copyOf(merged.values());
        }

        for (CapabilityDescriptor descriptor : capabilityRegistryService.list()) {
            String name = normalizeName(descriptor.name());
            if (descriptor.kind() == CapabilityKind.BACKEND) {
                BaseTool nativeTool = nativeToolsByName.get(name);
                if (nativeTool == null && missingBackendWarnings.add(name)) {
                    log.warn("Skip backend capability '{}' because no Java tool implementation is found", name);
                    continue;
                }
                merged.put(name, new BackendMetadataTool(nativeTool, descriptor));
                continue;
            }
            if (merged.containsKey(name)) {
                continue;
            }
            merged.put(name, new VirtualTool(descriptor));
        }
        return List.copyOf(merged.values());
    }

    public String toolCallType(String toolName) {
        CapabilityDescriptor descriptor = capability(toolName).orElse(null);
        if (descriptor == null) {
            return "function";
        }
        return switch (descriptor.kind()) {
            case FRONTEND -> normalize(descriptor.toolType(), "function");
            case ACTION -> "action";
            case BACKEND -> "function";
        };
    }

    public Optional<CapabilityDescriptor> capability(String toolName) {
        String normalizedName = normalizeName(toolName);
        if (normalizedName.isBlank()) {
            return Optional.empty();
        }
        if (capabilityRegistryService != null) {
            Optional<CapabilityDescriptor> dynamic = capabilityRegistryService.find(normalizedName);
            if (dynamic.isPresent()) {
                return dynamic;
            }
        }
        BaseTool nativeTool = nativeToolsByName.get(normalizedName);
        if (nativeTool == null) {
            return Optional.empty();
        }
        return Optional.of(new CapabilityDescriptor(
                normalizedName,
                nativeTool.description(),
                nativeTool.prompt(),
                nativeTool.parametersSchema(),
                false,
                CapabilityKind.BACKEND,
                "function",
                null,
                null,
                "java://builtin"
        ));
    }

    public boolean isAction(String toolName) {
        return "action".equalsIgnoreCase(toolCallType(toolName));
    }

    public boolean isFrontend(String toolName) {
        String type = toolCallType(toolName).toLowerCase(Locale.ROOT);
        return "html".equals(type) || "qlc".equals(type) || "dqlc".equals(type);
    }

    public String description(String toolName) {
        return capability(toolName).map(CapabilityDescriptor::description).orElse("");
    }

    private String normalizeName(String raw) {
        return normalize(raw, "").toLowerCase(Locale.ROOT);
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value.trim();
    }

    private Map<String, BaseTool> buildNativeToolsByName(List<BaseTool> tools) {
        return tools.stream().collect(Collectors.toMap(
                tool -> normalizeName(tool.name()),
                Function.identity(),
                (left, right) -> left,
                LinkedHashMap::new
        ));
    }

    private static final class VirtualTool implements BaseTool {
        private final CapabilityDescriptor descriptor;

        private VirtualTool(CapabilityDescriptor descriptor) {
            this.descriptor = descriptor;
        }

        @Override
        public String name() {
            return descriptor.name();
        }

        @Override
        public String description() {
            return descriptor.description();
        }

        @Override
        public String prompt() {
            return descriptor.prompt();
        }

        @Override
        public Map<String, Object> parametersSchema() {
            return descriptor.parameters();
        }

        @Override
        public JsonNode invoke(Map<String, Object> args) {
            throw new IllegalStateException("Virtual tool cannot be invoked directly: " + descriptor.name());
        }
    }

    private static final class BackendMetadataTool implements BaseTool {
        private final BaseTool delegate;
        private final CapabilityDescriptor descriptor;

        private BackendMetadataTool(BaseTool delegate, CapabilityDescriptor descriptor) {
            this.delegate = delegate;
            this.descriptor = descriptor;
        }

        @Override
        public String name() {
            return delegate.name();
        }

        @Override
        public String description() {
            return descriptor.description();
        }

        @Override
        public String prompt() {
            if (descriptor.prompt() != null && !descriptor.prompt().isBlank()) {
                return descriptor.prompt();
            }
            return delegate.prompt();
        }

        @Override
        public Map<String, Object> parametersSchema() {
            return descriptor.parameters();
        }

        @Override
        public JsonNode invoke(Map<String, Object> args) {
            return delegate.invoke(args);
        }
    }
}
