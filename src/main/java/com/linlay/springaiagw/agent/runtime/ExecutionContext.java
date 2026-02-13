package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.agent.AgentDefinition;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.model.AgentRequest;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.UserMessage;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public class ExecutionContext {

    private final AgentDefinition definition;
    private final AgentRequest request;
    private final Budget budget;
    private final long startedAtMs;

    private final List<Message> conversationMessages;
    private final List<Message> planMessages;
    private final List<Message> executeMessages;
    private final List<Map<String, Object>> toolRecords = new ArrayList<>();

    private int modelCalls;
    private int toolCalls;

    public ExecutionContext(AgentDefinition definition, AgentRequest request, List<Message> historyMessages) {
        this.definition = definition;
        this.request = request;
        this.budget = definition.runSpec().budget();
        this.startedAtMs = System.currentTimeMillis();

        this.conversationMessages = new ArrayList<>();
        if (historyMessages != null) {
            this.conversationMessages.addAll(historyMessages);
        }
        this.conversationMessages.add(new UserMessage(request.message()));

        this.planMessages = new ArrayList<>();
        if (historyMessages != null) {
            this.planMessages.addAll(historyMessages);
        }
        this.planMessages.add(new UserMessage(request.message()));

        this.executeMessages = new ArrayList<>();
        if (historyMessages != null) {
            this.executeMessages.addAll(historyMessages);
        }
        this.executeMessages.add(new UserMessage(request.message()));
    }

    public AgentDefinition definition() {
        return definition;
    }

    public AgentRequest request() {
        return request;
    }

    public Budget budget() {
        return budget;
    }

    public List<Message> conversationMessages() {
        return conversationMessages;
    }

    public List<Message> planMessages() {
        return planMessages;
    }

    public List<Message> executeMessages() {
        return executeMessages;
    }

    public List<Map<String, Object>> toolRecords() {
        return toolRecords;
    }

    public int modelCalls() {
        return modelCalls;
    }

    public int toolCalls() {
        return toolCalls;
    }

    public void incrementModelCalls() {
        this.modelCalls++;
        checkBudget();
    }

    public void incrementToolCalls(int count) {
        this.toolCalls += Math.max(0, count);
        checkBudget();
    }

    public void checkBudget() {
        if (modelCalls > budget.maxModelCalls()) {
            throw new RuntimeException("Budget exceeded: maxModelCalls=" + budget.maxModelCalls());
        }
        if (toolCalls > budget.maxToolCalls()) {
            throw new RuntimeException("Budget exceeded: maxToolCalls=" + budget.maxToolCalls());
        }
        if (System.currentTimeMillis() - startedAtMs >= budget.timeoutMs()) {
            throw new RuntimeException("Budget exceeded: timeoutMs=" + budget.timeoutMs());
        }
    }
}
