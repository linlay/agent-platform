package server

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
)

// runEventProcessor preserves the old package-local test vocabulary while
// production execution uses runtime/runexec. Keeping this adapter test-only
// prevents the extracted processor from drifting through duplicate runtime
// implementations.
type runEventProcessor struct {
	assistantText        *strings.Builder
	stepWriter           *chat.StepWriter
	billing              config.BillingConfig
	models               *models.ModelRegistry
	chatUsage            chat.UsageData
	runUsage             *chat.UsageData
	aggregateUsageByTask bool
	runControl           *contracts.RunControl
	runID                string
	chatID               string
	agentKey             string
	delegate             *runexec.Processor
}

func (p *runEventProcessor) processor() *runexec.Processor {
	if p.delegate == nil {
		p.delegate = runexec.NewProcessor(runexec.ProcessorOptions{
			AssistantText: p.assistantText, StepWriter: p.stepWriter,
			Billing: p.billing, Models: p.models, ChatUsage: p.chatUsage, RunUsage: p.runUsage,
			AggregateUsageByTask: p.aggregateUsageByTask, RunControl: p.runControl,
			RunID: p.runID, ChatID: p.chatID, AgentKey: p.agentKey,
			OnCompactEvent: func(data stream.EventData) { completeCompactControl(p.runControl, data) },
		})
	}
	return p.delegate
}

func (p *runEventProcessor) Consume(event stream.StreamEvent) (stream.EventData, bool, error) {
	return p.processor().Consume(event)
}

func (p *runEventProcessor) decorate(data *stream.EventData) {
	p.processor().Decorate(data)
}

func (p *runEventProcessor) beginModelTurn(taskID string) {
	p.processor().BeginModelTurn(taskID)
}

func (p *runEventProcessor) commitModelTurn(taskID string) {
	p.processor().CommitModelTurn(taskID)
}

func (p *runEventProcessor) discardModelTurn(taskID string, retrying bool) {
	p.processor().DiscardModelTurn(taskID, retrying)
}

func (p *runEventProcessor) terminalFinishReason() string {
	return p.processor().TerminalFinishReason()
}

func (p *runEventProcessor) terminalErrorPayload() map[string]any {
	return p.processor().TerminalErrorPayload()
}
