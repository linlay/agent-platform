package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.scheduling.annotation.Scheduled;
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
    private final DeltaStreamService deltaStreamService;
    private final ToolRegistry toolRegistry;
    private final ObjectMapper objectMapper;

    private final Object reloadLock = new Object();
    private volatile Map<String, Agent> agents = Map.of();

    public AgentRegistry(
            AgentDefinitionLoader definitionLoader,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper
    ) {
        this.definitionLoader = definitionLoader;
        this.llmService = llmService;
        this.deltaStreamService = deltaStreamService;
        this.toolRegistry = toolRegistry;
        this.objectMapper = objectMapper;
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

    @Scheduled(fixedDelayString = "${agent.catalog.refresh-interval-ms:10000}")
    public void refreshAgents() {
        synchronized (reloadLock) {
            try {
                List<AgentDefinition> definitions = definitionLoader.loadAll();
                Map<String, Agent> updated = new LinkedHashMap<>();
                for (AgentDefinition definition : definitions) {
                    Agent agent = new DefinitionDrivenAgent(definition, llmService, deltaStreamService, toolRegistry, objectMapper);
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
