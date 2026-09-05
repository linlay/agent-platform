package stream

func (e StreamEvent) ToData() map[string]any {
	return e.Data().Map()
}

func (a *StreamEventAssembler) Bootstrap() []StreamEvent {
	return visibleEmissionEvents(a.BootstrapEmissions())
}

func (a *StreamEventAssembler) Consume(input StreamInput) []StreamEvent {
	return visibleEmissionEvents(a.ConsumeEmissions(input))
}

func (a *StreamEventAssembler) Complete() []StreamEvent {
	return visibleEmissionEvents(a.CompleteEmissions())
}

func (a *StreamEventAssembler) Fail(err error) []StreamEvent {
	return visibleEmissionEvents(a.FailEmissions(err))
}

func visibleEmissionEvents(emissions []EventEmission) []StreamEvent {
	if len(emissions) == 0 {
		return nil
	}
	events := make([]StreamEvent, 0, len(emissions))
	for _, emission := range emissions {
		if emission.Visible {
			events = append(events, emission.Event)
		}
	}
	return events
}

func (n *SseEventNormalizer) Normalize(events []StreamEvent) []StreamEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]StreamEvent, 0, len(events))
	for _, event := range events {
		if n.IsVisible(event) {
			out = append(out, event)
		}
	}
	return out
}
