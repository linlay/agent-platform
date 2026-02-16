package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.agent.AgentDefinition;
import com.linlay.springaiagw.agent.PlannedToolCall;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.skill.SkillDescriptor;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.UserMessage;
import org.springframework.util.StringUtils;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

public class ExecutionContext {

    private static final Logger log = LoggerFactory.getLogger(ExecutionContext.class);
    private static final String SKILL_SCRIPT_RUN_TOOL = "_skill_run_script_";

    private final AgentDefinition definition;
    private final AgentRequest request;
    private final Budget budget;
    private final long startedAtMs;
    private final String skillCatalogPrompt;
    private final Map<String, SkillDescriptor> resolvedSkillsById;

    private final List<Message> conversationMessages;
    private final List<Message> planMessages;
    private final List<Message> executeMessages;
    private final List<Map<String, Object>> toolRecords = new ArrayList<>();
    private final List<AgentDelta.PlanTask> planTasks = new ArrayList<>();
    private final Set<String> pendingSkillIds = new LinkedHashSet<>();
    private final Set<String> disclosedSkillIds = new LinkedHashSet<>();
    private String planId;

    private int modelCalls;
    private int toolCalls;

    public ExecutionContext(AgentDefinition definition, AgentRequest request, List<Message> historyMessages) {
        this(definition, request, historyMessages, "", Map.of());
    }

    public ExecutionContext(
            AgentDefinition definition,
            AgentRequest request,
            List<Message> historyMessages,
            String skillPrompt
    ) {
        this(definition, request, historyMessages, skillPrompt, Map.of());
    }

    public ExecutionContext(
            AgentDefinition definition,
            AgentRequest request,
            List<Message> historyMessages,
            String skillCatalogPrompt,
            Map<String, SkillDescriptor> resolvedSkillsById
    ) {
        this.definition = definition;
        this.request = request;
        this.budget = definition.runSpec().budget();
        this.startedAtMs = System.currentTimeMillis();
        this.skillCatalogPrompt = StringUtils.hasText(skillCatalogPrompt) ? skillCatalogPrompt.trim() : "";
        this.resolvedSkillsById = normalizeResolvedSkills(resolvedSkillsById);

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

    public List<String> skills() {
        return definition.skills();
    }

    public String skillPrompt() {
        return skillCatalogPrompt;
    }

    public String skillCatalogPrompt() {
        return skillCatalogPrompt;
    }

    public String stageSystemPrompt(String stageSystemPrompt) {
        if (!StringUtils.hasText(skillCatalogPrompt)) {
            return stageSystemPrompt == null ? "" : stageSystemPrompt;
        }
        if (!StringUtils.hasText(stageSystemPrompt)) {
            return skillCatalogPrompt;
        }
        return stageSystemPrompt + "\n\n" + skillCatalogPrompt;
    }

    public void registerSkillUsageFromToolCalls(List<PlannedToolCall> toolCalls) {
        if (toolCalls == null || toolCalls.isEmpty() || resolvedSkillsById.isEmpty()) {
            return;
        }
        for (PlannedToolCall call : toolCalls) {
            if (call == null || !SKILL_SCRIPT_RUN_TOOL.equals(normalizeToolName(call.name()))) {
                continue;
            }
            String skillId = normalizeSkillId(readSkillId(call.arguments()));
            if (!StringUtils.hasText(skillId)) {
                continue;
            }
            if (!resolvedSkillsById.containsKey(skillId)) {
                log.warn(
                        "[agent:{}] tool _skill_run_script_ requested unknown skill and will be ignored: {}",
                        definition.id(),
                        skillId
                );
                continue;
            }
            if (disclosedSkillIds.contains(skillId)) {
                continue;
            }
            pendingSkillIds.add(skillId);
        }
    }

    public String consumeDeferredSkillUserPrompt() {
        if (pendingSkillIds.isEmpty()) {
            return "";
        }
        List<String> toConsume = new ArrayList<>(pendingSkillIds);
        pendingSkillIds.clear();

        List<String> blocks = new ArrayList<>();
        for (String skillId : toConsume) {
            disclosedSkillIds.add(skillId);
            SkillDescriptor descriptor = resolvedSkillsById.get(skillId);
            if (descriptor == null) {
                continue;
            }
            String promptBlock = buildSkillDisclosureBlock(descriptor);
            if (StringUtils.hasText(promptBlock)) {
                blocks.add(promptBlock);
            }
        }
        if (blocks.isEmpty()) {
            return "";
        }
        return "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:\n\n"
                + String.join("\n\n---\n\n", blocks);
    }

    public String planId() {
        if (StringUtils.hasText(planId)) {
            return planId;
        }
        String chatId = request.chatId();
        if (StringUtils.hasText(chatId)) {
            String stable = UUID.nameUUIDFromBytes(chatId.trim().getBytes(StandardCharsets.UTF_8))
                    .toString()
                    .replace("-", "");
            this.planId = "plan_" + stable.substring(0, 12);
            return this.planId;
        }
        String runId = request.runId();
        if (StringUtils.hasText(runId)) {
            String normalized = runId.trim().replace("-", "");
            this.planId = "plan_" + normalized.substring(0, Math.min(12, normalized.length()));
            return this.planId;
        }
        this.planId = "plan_default";
        return this.planId;
    }

    public List<AgentDelta.PlanTask> planTasks() {
        return List.copyOf(planTasks);
    }

    public boolean hasPlan() {
        return !planTasks.isEmpty();
    }

    public void initializePlan(String planId, List<AgentDelta.PlanTask> tasks) {
        if (StringUtils.hasText(planId)) {
            this.planId = planId.trim();
        }
        this.planTasks.clear();
        if (tasks == null || tasks.isEmpty()) {
            return;
        }
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null || !StringUtils.hasText(task.taskId()) || !StringUtils.hasText(task.description())) {
                continue;
            }
            this.planTasks.add(new AgentDelta.PlanTask(
                    task.taskId().trim(),
                    task.description().trim(),
                    normalizeStatus(task.status())
            ));
        }
    }

    public void appendPlanTasks(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null || tasks.isEmpty()) {
            return;
        }
        Set<String> usedIds = new LinkedHashSet<>();
        for (AgentDelta.PlanTask task : planTasks) {
            if (task != null && StringUtils.hasText(task.taskId())) {
                usedIds.add(task.taskId().trim());
            }
        }
        int nextIndex = usedIds.size();
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null || !StringUtils.hasText(task.description())) {
                continue;
            }
            String taskId = StringUtils.hasText(task.taskId()) ? task.taskId().trim() : "";
            if (!StringUtils.hasText(taskId) || usedIds.contains(taskId)) {
                taskId = shortId();
                while (usedIds.contains(taskId)) {
                    taskId = shortId();
                }
            }
            nextIndex++;
            usedIds.add(taskId);
            planTasks.add(new AgentDelta.PlanTask(
                    taskId,
                    task.description().trim(),
                    normalizeStatus(task.status())
            ));
        }
    }

    public boolean updatePlanTask(String taskId, String status, String description) {
        if (!StringUtils.hasText(taskId)) {
            return false;
        }
        String normalizedTaskId = taskId.trim();
        for (int i = 0; i < planTasks.size(); i++) {
            AgentDelta.PlanTask task = planTasks.get(i);
            if (task == null || !normalizedTaskId.equals(task.taskId())) {
                continue;
            }
            String nextDescription = StringUtils.hasText(description) ? description.trim() : task.description();
            String nextStatus = normalizeStatus(status);
            planTasks.set(i, new AgentDelta.PlanTask(task.taskId(), nextDescription, nextStatus));
            return true;
        }
        return false;
    }

    public AgentDelta.PlanUpdate snapshotPlanUpdate() {
        return new AgentDelta.PlanUpdate(planId(), request.chatId(), planTasks());
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

    private String normalizeStatus(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase(Locale.ROOT);
        return switch (normalized) {
            case "in_progress" -> "init";
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private String shortId() {
        return UUID.randomUUID().toString().replace("-", "").substring(0, 8).toLowerCase(Locale.ROOT);
    }

    private Map<String, SkillDescriptor> normalizeResolvedSkills(Map<String, SkillDescriptor> raw) {
        if (raw == null || raw.isEmpty()) {
            return Map.of();
        }
        Map<String, SkillDescriptor> normalized = new LinkedHashMap<>();
        for (Map.Entry<String, SkillDescriptor> entry : raw.entrySet()) {
            SkillDescriptor descriptor = entry.getValue();
            if (descriptor == null) {
                continue;
            }
            String skillId = normalizeSkillId(descriptor.id());
            if (!StringUtils.hasText(skillId)) {
                skillId = normalizeSkillId(entry.getKey());
            }
            if (!StringUtils.hasText(skillId)) {
                continue;
            }
            normalized.putIfAbsent(skillId, descriptor);
        }
        return normalized.isEmpty() ? Map.of() : Map.copyOf(normalized);
    }

    private String buildSkillDisclosureBlock(SkillDescriptor descriptor) {
        String prompt = descriptor.prompt() == null ? "" : descriptor.prompt().trim();
        if (!StringUtils.hasText(prompt)) {
            return "";
        }
        StringBuilder block = new StringBuilder();
        block.append("skillId: ").append(descriptor.id());
        if (StringUtils.hasText(descriptor.name())) {
            block.append("\nname: ").append(descriptor.name());
        }
        if (StringUtils.hasText(descriptor.description())) {
            block.append("\ndescription: ").append(descriptor.description());
        }
        block.append("\ninstructions:\n").append(prompt);
        return block.toString();
    }

    private String readSkillId(Map<String, Object> args) {
        if (args == null || args.isEmpty()) {
            return "";
        }
        Object raw = args.get("skill");
        if (raw == null) {
            return "";
        }
        return raw.toString();
    }

    private String normalizeToolName(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "";
        }
        return raw.trim().toLowerCase(Locale.ROOT);
    }

    private String normalizeSkillId(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "";
        }
        return raw.trim().toLowerCase(Locale.ROOT);
    }
}
