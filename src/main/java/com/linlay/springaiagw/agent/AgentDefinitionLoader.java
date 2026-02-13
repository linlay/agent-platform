package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.config.ChatClientRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Stream;

@Component
public class AgentDefinitionLoader {

    private static final Logger log = LoggerFactory.getLogger(AgentDefinitionLoader.class);
    private static final Pattern SYSTEM_PROMPT_MULTILINE_PATTERN =
            Pattern.compile("(\"systemPrompt\"\\s*:\\s*)\"\"\"([\\s\\S]*?)\"\"\"");

    private final ObjectMapper objectMapper;
    private final AgentCatalogProperties properties;
    private final ChatClientRegistry chatClientRegistry;

    @Deprecated
    public AgentDefinitionLoader(ObjectMapper objectMapper, AgentCatalogProperties properties) {
        this(objectMapper, properties, null);
    }

    @Autowired
    public AgentDefinitionLoader(ObjectMapper objectMapper, AgentCatalogProperties properties, ChatClientRegistry chatClientRegistry) {
        this.objectMapper = objectMapper;
        this.properties = properties;
        this.chatClientRegistry = chatClientRegistry;
    }

    public List<AgentDefinition> loadAll() {
        Map<String, AgentDefinition> definitions = new LinkedHashMap<>();
        builtInAgents().forEach(agent -> definitions.put(agent.id(), agent));
        loadExternalAgents().forEach(agent -> {
            AgentDefinition old = definitions.put(agent.id(), agent);
            if (old != null) {
                log.info("External agent '{}' overrides existing definition", agent.id());
            }
        });
        return List.copyOf(definitions.values());
    }

    private List<AgentDefinition> builtInAgents() {
        return List.of(
                new AgentDefinition(
                        "demoPlain",
                        "默认示例：PLAIN 模式按需单次调用工具",
                        "bailian",
                        "qwen3-max",
                        "你是简洁的助理。严格使用原生 Function Calling：需要工具时发起 tool_calls；不需要工具时直接给可执行结论（120 字以内）。",
                        AgentMode.PLAIN,
                        List.of("mock_sensitive_data_detector", "city_datetime", "mock_city_weather")
                ),
                new AgentDefinition(
                        "demoReAct",
                        "默认示例：RE-ACT 模式按需调用工具",
                        "bailian",
                        "qwen3-max",
                        "你是 RE-ACT 助手。严格使用原生 Function Calling：每轮最多一个 tool_call；需要工具就调用，不需要工具就直接输出最终结论。",
                        AgentMode.RE_ACT,
                        List.of("city_datetime", "mock_city_weather", "mock_sensitive_data_detector", "bash")
                ),
                new AgentDefinition(
                        "demoPlanExecute",
                        "默认示例：PLAN-EXECUTE 模式先规划后执行工具",
                        "bailian",
                        "qwen3-max",
                        "你是高级规划助手。严格使用原生 Function Calling：需要工具时用 tool_calls 顺序执行，不在正文输出工具调用 JSON，最后给简洁总结。",
                        AgentMode.PLAN_EXECUTE,
                        List.of("mock_ops_runbook", "city_datetime", "mock_city_weather", "mock_sensitive_data_detector", "bash")
                ),
                new AgentDefinition(
                        "demoViewport",
                        "内置示例：先查时间与天气，再输出 viewport 协议代码块",
                        "bailian",
                        "qwen3-max",
                        "你是天气视图助手。必须先通过工具获取数据，再输出 viewport 协议代码块。"
                                + "最终回答必须且只能是一个以 ```viewport 开头的 fenced block，不得附加自然语言。"
                                + "输出模板：```viewport\\n"
                                + "type=<dynamic_type>, key=<dynamic_key>\\n"
                                + "{...json...}\\n```。"
                                + "当无法判断 type/key 时，使用 type=html, key=show_weather_card。"
                                + "JSON 必须是 mock_city_weather 的结果对象，包含 city,date,temperatureC,humidity,windLevel,condition,mockTag。",
                        AgentMode.PLAN_EXECUTE,
                        List.of("city_datetime", "mock_city_weather")
                ),
                new AgentDefinition(
                        "demoAction",
                        "内置示例：主题切换动作智能体",
                        "bailian",
                        "qwen3-max",
                        "你是主题切换助手。用户提出主题切换需求时，必须调用 switch_theme。"
                                + "theme 仅允许 light 或 dark。"
                                + "工具执行后输出一句简短确认。",
                        AgentMode.PLAIN,
                        List.of("switch_theme")
                ),
                new AgentDefinition(
                        "agentCreator",
                        "内置智能体：根据需求创建 agents 目录下的智能体配置",
                        "bailian",
                        "qwen3-max",
                        "你是 Agent 创建助手。目标是把用户需求转成智能体配置，并调用工具创建到 agents 目录。"
                                + "严格使用原生 Function Calling，不在正文输出工具调用 JSON；缺失字段用最小合理默认值，并在最终回答中说明。",
                        AgentMode.PLAN_EXECUTE,
                        List.of("agent_file_create")
                )
        );
    }

    private List<AgentDefinition> loadExternalAgents() {
        Path dir = Path.of(properties.getExternalDir()).toAbsolutePath().normalize();
        if (!Files.exists(dir)) {
            log.debug("External agents dir does not exist, skip loading: {}", dir);
            return List.of();
        }
        if (!Files.isDirectory(dir)) {
            log.warn("Configured external agents path is not a directory: {}", dir);
            return List.of();
        }

        List<AgentDefinition> loaded = new ArrayList<>();
        try (Stream<Path> stream = Files.list(dir)) {
            stream.filter(path -> Files.isRegularFile(path) && path.getFileName().toString().endsWith(".json"))
                    .sorted(Comparator.comparing(path -> path.getFileName().toString()))
                    .forEach(path -> tryLoadExternal(path).ifPresent(loaded::add));
        } catch (IOException ex) {
            log.warn("Cannot list external agents from {}", dir, ex);
        }

        if (!loaded.isEmpty()) {
            log.debug("Loaded {} external agents from {}", loaded.size(), dir);
        }
        return loaded;
    }

    private java.util.Optional<AgentDefinition> tryLoadExternal(Path file) {
        String fileName = file.getFileName().toString();
        String agentId = fileName.substring(0, fileName.length() - ".json".length()).trim();
        if (agentId.isEmpty()) {
            log.warn("Skip external agent with empty name: {}", file);
            return java.util.Optional.empty();
        }

        try {
            AgentConfigFile config = readAgentConfig(file);
            String providerKey = resolveProviderKey(config);
            AgentMode mode = resolveMode(config.getMode(), config.getDeepThink());
            String model = normalize(config.getModel(), resolveDefaultModel(providerKey));
            String systemPrompt = normalize(config.getSystemPrompt(), "你是通用助理，回答要清晰和可执行。");
            String description = normalize(config.getDescription(), "external agent from " + fileName);
            List<String> tools = normalizeToolNames(config.getTools());

            return java.util.Optional.of(new AgentDefinition(
                    agentId,
                    description,
                    providerKey,
                    model,
                    systemPrompt,
                    mode,
                    tools
            ));
        } catch (IOException ex) {
            log.warn("Skip invalid external agent file: {}", file, ex);
            return java.util.Optional.empty();
        }
    }

    private AgentConfigFile readAgentConfig(Path file) throws IOException {
        String raw = Files.readString(file);
        String normalized = normalizeMultilineSystemPrompt(raw);
        return objectMapper.readValue(normalized, AgentConfigFile.class);
    }

    private String normalizeMultilineSystemPrompt(String rawJson) throws IOException {
        Matcher matcher = SYSTEM_PROMPT_MULTILINE_PATTERN.matcher(rawJson);
        if (!matcher.find()) {
            return rawJson;
        }

        StringBuffer rewritten = new StringBuffer();
        do {
            String content = stripOuterLineBreak(matcher.group(2));
            String escaped = objectMapper.writeValueAsString(content);
            matcher.appendReplacement(rewritten, Matcher.quoteReplacement(matcher.group(1) + escaped));
        } while (matcher.find());
        matcher.appendTail(rewritten);
        return rewritten.toString();
    }

    private String stripOuterLineBreak(String content) {
        String normalized = content.replace("\r\n", "\n");
        if (normalized.startsWith("\n")) {
            normalized = normalized.substring(1);
        }
        if (normalized.endsWith("\n")) {
            normalized = normalized.substring(0, normalized.length() - 1);
        }
        return normalized;
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    private String resolveProviderKey(AgentConfigFile config) {
        if (config.getProviderKey() != null && !config.getProviderKey().isBlank()) {
            return config.getProviderKey().trim().toLowerCase(Locale.ROOT);
        }
        if (config.getProviderType() != null && !config.getProviderType().isBlank()) {
            return config.getProviderType().trim().toLowerCase(Locale.ROOT);
        }
        return "bailian";
    }

    private String resolveDefaultModel(String providerKey) {
        if (chatClientRegistry != null) {
            String dynamicModel = chatClientRegistry.defaultModel(providerKey);
            if (dynamicModel != null && !dynamicModel.isBlank()) {
                return dynamicModel;
            }
        }
        if ("siliconflow".equalsIgnoreCase(providerKey)) {
            return "deepseek-ai/DeepSeek-V3.2";
        }
        return "qwen3-max";
    }

    private AgentMode resolveMode(AgentMode mode, Boolean deepThink) {
        if (mode != null) {
            return mode;
        }
        if (deepThink == null) {
            return AgentMode.PLAIN;
        }
        return deepThink ? AgentMode.PLAN_EXECUTE : AgentMode.PLAIN;
    }

    private List<String> normalizeToolNames(List<String> rawTools) {
        if (rawTools == null || rawTools.isEmpty()) {
            return List.of();
        }
        List<String> tools = new ArrayList<>();
        for (String raw : rawTools) {
            if (raw == null || raw.isBlank()) {
                continue;
            }
            tools.add(raw.trim().toLowerCase(Locale.ROOT));
        }
        return List.copyOf(tools);
    }
}
