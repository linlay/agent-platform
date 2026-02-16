package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.service.RuntimeResourceSyncService;
import com.linlay.springaiagw.skill.SkillRegistryService;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Comparator;

@Component
public class AgentRegistry {

    private static final Logger log = LoggerFactory.getLogger(AgentRegistry.class);

    private final AgentDefinitionLoader definitionLoader;
    private final LlmService llmService;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper;
    private final ChatWindowMemoryStore chatWindowMemoryStore;
    private final FrontendSubmitCoordinator frontendSubmitCoordinator;
    private final SkillRegistryService skillRegistryService;
    @SuppressWarnings("unused")
    private final RuntimeResourceSyncService runtimeResourceSyncService;

    private final Object reloadLock = new Object();
    private volatile Map<String, Agent> agents = Map.of();

    @Autowired
    public AgentRegistry(
            AgentDefinitionLoader definitionLoader,
            LlmService llmService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore,
            FrontendSubmitCoordinator frontendSubmitCoordinator,
            RuntimeResourceSyncService runtimeResourceSyncService,
            SkillRegistryService skillRegistryService
    ) {
        this.definitionLoader = definitionLoader;
        this.llmService = llmService;
        this.toolRegistry = toolRegistry;
        this.objectMapper = objectMapper;
        this.chatWindowMemoryStore = chatWindowMemoryStore;
        this.frontendSubmitCoordinator = frontendSubmitCoordinator;
        this.runtimeResourceSyncService = runtimeResourceSyncService;
        this.skillRegistryService = skillRegistryService;
        refreshAgents();
    }

    public Agent get(String id) {
        Map<String, Agent> snapshot = agents;
        Agent agent = snapshot.get(id);
        if (agent == null) {
            throw new IllegalArgumentException("Unknown agentId: " + id + ". Available: " + snapshot.keySet().stream().sorted().toList());
        }
        return agent;
    }

    public List<String> listIds() {
        return agents.keySet().stream().sorted().toList();
    }

    public List<Agent> list() {
        return agents.values().stream()
                .sorted(Comparator.comparing(Agent::id))
                .toList();
    }

    public Agent defaultAgent() {
        List<Agent> current = list();
        if (current.isEmpty()) {
            throw new IllegalArgumentException("No agents available");
        }
        return current.getFirst();
    }

    public void refreshAgents() {
        synchronized (reloadLock) {
            try {
                List<AgentDefinition> definitions = definitionLoader.loadAll();
                Map<String, Agent> updated = new LinkedHashMap<>();
                for (AgentDefinition definition : definitions) {
                    Agent agent = new DefinitionDrivenAgent(
                            definition,
                            llmService,
                            toolRegistry,
                            objectMapper,
                            chatWindowMemoryStore,
                            frontendSubmitCoordinator,
                            skillRegistryService
                    );
                    updated.put(agent.id(), agent);
                }
                this.agents = Map.copyOf(updated);
                log.debug("Refreshed agents cache, size={}", updated.size());
            } catch (Exception ex) {
                log.warn("Failed to refresh agents cache, keep previous snapshot", ex);
            }
        }
    }
}
