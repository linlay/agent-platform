package runexec

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/observability"
	"agent-platform/internal/stream"
)

type ProcessorOptions struct {
	AssistantText        *strings.Builder
	StepWriter           *chat.StepWriter
	Billing              config.BillingConfig
	Models               *models.ModelRegistry
	ChatUsage            chat.UsageData
	RunUsage             *chat.UsageData
	AggregateUsageByTask bool
	RunControl           *contracts.RunControl
	RunID                string
	ChatID               string
	AgentKey             string
	OnCompactEvent       func(stream.EventData)
}

// Processor consumes assembled run events, persists StepLines, aggregates
// usage, and records the authoritative terminal state. It is transport-free:
// SSE and WebSocket publication remain outside this component.
type Processor struct {
	assistantText        *strings.Builder
	modelTurnPending     bool
	modelTurnText        strings.Builder
	stepWriter           *chat.StepWriter
	billing              config.BillingConfig
	models               *models.ModelRegistry
	chatUsage            chat.UsageData
	runUsage             *chat.UsageData
	runModelKey          string
	runModelMixed        bool
	aggregateUsageByTask bool
	taskRunUsage         map[string]chat.UsageData
	runControl           *contracts.RunControl
	runID                string
	chatID               string
	agentKey             string
	terminalType         string
	terminalError        map[string]any
	onCompactEvent       func(stream.EventData)
}

func NewProcessor(options ProcessorOptions) *Processor {
	return &Processor{
		assistantText: options.AssistantText, stepWriter: options.StepWriter,
		billing: options.Billing, models: options.Models, chatUsage: options.ChatUsage,
		runUsage: options.RunUsage, aggregateUsageByTask: options.AggregateUsageByTask,
		runControl: options.RunControl, runID: options.RunID, chatID: options.ChatID,
		agentKey: options.AgentKey, onCompactEvent: options.OnCompactEvent,
	}
}

func (p *Processor) Consume(event stream.StreamEvent) (stream.EventData, bool, error) {
	data := event.Data()
	if err := stream.ValidateEventData(data, "run.executor.event"); err != nil {
		return stream.EventData{}, false, err
	}
	p.Decorate(&data)
	p.recordTerminal(data)
	if p.stepWriter != nil {
		p.stepWriter.OnEvent(data)
		if err := p.stepWriter.Err(); err != nil {
			return data, false, err
		}
	}
	if p.onCompactEvent != nil && (data.Type == "context.compact.complete" || data.Type == "context.compact.failed") {
		p.onCompactEvent(data)
	}
	return data, stream.IsClientVisibleEventData(data), nil
}

func (p *Processor) recordTerminal(data stream.EventData) {
	if p == nil || p.terminalType != "" || strings.TrimSpace(data.String("taskId")) != "" {
		return
	}
	switch data.Type {
	case "run.complete", "run.cancel", "run.error":
		p.terminalType = data.Type
	default:
		return
	}
	if data.Type != "run.error" {
		return
	}
	if payload, ok := data.Payload["error"].(map[string]any); ok {
		p.terminalError = contracts.CloneMap(payload)
	}
	if p.runControl != nil {
		p.runControl.ClaimFailure()
	}
	p.logTerminalError(data)
}

func (p *Processor) TerminalFinishReason() string {
	if p == nil {
		return ""
	}
	switch p.terminalType {
	case "run.error":
		return "error"
	case "run.cancel":
		return "cancel"
	case "run.complete":
		return "complete"
	default:
		return ""
	}
}

func (p *Processor) TerminalErrorPayload() map[string]any {
	if p == nil {
		return nil
	}
	return contracts.CloneMap(p.terminalError)
}

func (p *Processor) logTerminalError(data stream.EventData) {
	errorPayload, _ := data.Payload["error"].(map[string]any)
	diagnostics, _ := errorPayload["diagnostics"].(map[string]any)
	fields := map[string]any{
		"runId":     firstNonBlank(strings.TrimSpace(p.runID), data.String("runId")),
		"chatId":    strings.TrimSpace(p.chatID),
		"agentKey":  strings.TrimSpace(p.agentKey),
		"errorCode": strings.TrimSpace(contracts.AnyStringNode(errorPayload["code"])),
	}
	for _, key := range []string{"toolCalls", "limitValue", "limitName", "toolName"} {
		if value, ok := diagnostics[key]; ok {
			fields[key] = value
		}
	}
	observability.Log("run.error", fields)
}

func (p *Processor) Decorate(data *stream.EventData) {
	if data == nil {
		return
	}
	switch data.Type {
	case "content.delta":
		if strings.TrimSpace(data.String("taskId")) != "" {
			return
		}
		if p.assistantText != nil {
			if delta := data.String("delta"); delta != "" {
				if p.modelTurnPending {
					p.modelTurnText.WriteString(delta)
				} else {
					p.assistantText.WriteString(delta)
				}
			}
		}
	case "content.snapshot":
		if strings.TrimSpace(data.String("taskId")) != "" {
			return
		}
		if p.assistantText != nil {
			if text := data.String("text"); text != "" {
				if p.modelTurnPending {
					p.modelTurnText.Reset()
					p.modelTurnText.WriteString(text)
				} else {
					p.assistantText.Reset()
					p.assistantText.WriteString(text)
				}
			}
		}
	case "debug.llmChat":
		inner, ok := data.Payload["data"].(map[string]any)
		if !ok {
			return
		}
		usage, ok := inner["usage"].(map[string]any)
		if !ok {
			usage = map[string]any{}
			inner["usage"] = usage
		}
		(UsageCostDecorator{Models: p.models, Billing: p.billing}).DecorateDebugLLMReturnUsage(inner)
		if p.runUsage != nil && !p.aggregateUsageByTask {
			if run, ok := usage["runUsage"].(map[string]any); ok {
				MergeUsageMapIntoRunData(p.runUsage, run)
				p.applyRunModelKey()
			}
		}
		if p.runUsage != nil {
			chatUsage := AddUsageData(p.chatUsage, *p.runUsage)
			chatUsage.ModelKey = ""
			usage["chatUsage"] = UsageDataMap(chatUsage)
		}
	case "usage.snapshot":
		if _, ok := data.Payload["usage"].(map[string]any); !ok {
			return
		}
		p.decorateUsageSnapshot(data)
		if p.runUsage != nil {
			usage, _ := data.Payload["usage"].(map[string]any)
			chatUsage := AddUsageData(p.chatUsage, *p.runUsage)
			chatUsage.ModelKey = ""
			usage["chat"] = UsageDataMapForSnapshot(chatUsage)
		}
	case "run.complete", "run.error", "run.cancel":
		if p.runUsage != nil && !p.aggregateUsageByTask {
			if usage, ok := data.Payload["usage"].(map[string]any); ok {
				if run, ok := usage["run"].(map[string]any); ok {
					MergeUsageMapIntoRunData(p.runUsage, run)
				} else {
					MergeUsageMapIntoRunData(p.runUsage, usage)
				}
			}
		}
		p.decorateTerminalUsage(data)
	}
}

func (p *Processor) BeginModelTurn(taskID string) {
	if p == nil || strings.TrimSpace(taskID) != "" {
		return
	}
	p.modelTurnPending = true
	p.modelTurnText.Reset()
}

func (p *Processor) CommitModelTurn(taskID string) {
	if p == nil || strings.TrimSpace(taskID) != "" || !p.modelTurnPending {
		return
	}
	if p.assistantText != nil {
		p.assistantText.Reset()
		p.assistantText.WriteString(p.modelTurnText.String())
	}
	p.modelTurnText.Reset()
	p.modelTurnPending = false
}

func (p *Processor) DiscardModelTurn(taskID string, _ bool) {
	if p == nil || strings.TrimSpace(taskID) != "" {
		return
	}
	p.modelTurnText.Reset()
	p.modelTurnPending = true
}

func (p *Processor) ApplyModelTurnControl(input stream.StreamInput) {
	if p == nil || input == nil {
		return
	}
	switch value := input.(type) {
	case stream.InputLLMRequest:
		p.BeginModelTurn(value.TaskID)
	case stream.ModelTurnCommit:
		p.CommitModelTurn(value.TaskID)
		if p.stepWriter != nil {
			p.stepWriter.CommitModelTurn(value.TaskID, value.RunSeq)
		}
	case stream.ModelTurnDiscard:
		p.DiscardModelTurn(value.TaskID, value.Retrying)
		if p.stepWriter != nil {
			p.stepWriter.DiscardModelTurn(value.TaskID, value.RunSeq, value.Retrying)
		}
	}
}

func (p *Processor) decorateUsageSnapshot(data *stream.EventData) {
	if p == nil || data == nil {
		return
	}
	usage, _ := data.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	var (
		currentUsage chat.UsageData
		hasCurrent   bool
	)
	if current, _ := usage["current"].(map[string]any); current != nil {
		currentUsage, hasCurrent = (UsageCostDecorator{Models: p.models, Billing: p.billing}).DecorateCurrentUsage(data)
		if modelKey := strings.TrimSpace(currentUsage.ModelKey); modelKey != "" {
			p.recordRunModelKey(modelKey)
		}
	}
	if p.aggregateUsageByTask {
		p.decorateAggregatedTaskUsageSnapshot(data, usage, currentUsage)
		return
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		if p.runUsage != nil {
			if UsageEstimatedCostFromData(currentUsage) != nil {
				AddEstimatedUsageCost(p.runUsage, currentUsage)
			}
			MergeUsageMapIntoRunData(p.runUsage, run)
			p.applyRunModelKey()
			runUsage := *p.runUsage
			runUsage.ModelKey = ""
			usage["run"] = UsageDataMapForSnapshot(runUsage)
		}
	} else if hasCurrent && p.runUsage != nil {
		*p.runUsage = AddUsageData(*p.runUsage, currentUsage)
		p.applyRunModelKey()
		runUsage := *p.runUsage
		runUsage.ModelKey = ""
		usage["run"] = UsageDataMapForSnapshot(runUsage)
	}
}

func (p *Processor) decorateAggregatedTaskUsageSnapshot(data *stream.EventData, usage map[string]any, currentUsage chat.UsageData) {
	if p == nil || p.runUsage == nil || data == nil || usage == nil {
		return
	}
	if p.taskRunUsage == nil {
		p.taskRunUsage = map[string]chat.UsageData{}
	}
	key := strings.TrimSpace(data.String("taskId"))
	if key == "" {
		key = "__team_coordinator__"
	}
	accumulated := p.taskRunUsage[key]
	if UsageEstimatedCostFromData(currentUsage) != nil {
		AddEstimatedUsageCost(&accumulated, currentUsage)
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		MergeRunUsageData(&accumulated, UsageDataFromMap(run))
	} else {
		accumulated = AddUsageData(accumulated, currentUsage)
	}
	p.taskRunUsage[key] = accumulated
	total := chat.UsageData{}
	for _, taskUsage := range p.taskRunUsage {
		total = AddUsageData(total, taskUsage)
	}
	*p.runUsage = total
	p.applyRunModelKey()
	runUsage := *p.runUsage
	runUsage.ModelKey = ""
	usage["run"] = UsageDataMapForSnapshot(runUsage)
}

func (p *Processor) recordRunModelKey(modelKey string) {
	if p == nil || p.runModelMixed {
		return
	}
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return
	}
	if p.runModelKey == "" {
		p.runModelKey = modelKey
		return
	}
	if p.runModelKey != modelKey {
		p.runModelKey = ""
		p.runModelMixed = true
	}
}

func (p *Processor) applyRunModelKey() {
	if p == nil || p.runUsage == nil {
		return
	}
	if p.runModelMixed {
		p.runUsage.ModelKey = ""
		return
	}
	if p.runModelKey != "" {
		p.runUsage.ModelKey = p.runModelKey
	}
}

func (p *Processor) decorateTerminalUsage(data *stream.EventData) {
	if p == nil || data == nil || data.Payload == nil {
		return
	}
	delete(data.Payload, "chatUsage")
	if p.runUsage == nil || !UsageHasData(*p.runUsage) {
		delete(data.Payload, "usage")
		return
	}
	p.applyRunModelKey()
	chatUsage := AddUsageData(p.chatUsage, *p.runUsage)
	chatUsage.ModelKey = ""
	runUsage := *p.runUsage
	runUsage.ModelKey = ""
	data.Payload["usage"] = map[string]any{
		"chat": UsageDataMap(chatUsage),
		"run":  UsageDataMap(runUsage),
	}
}
