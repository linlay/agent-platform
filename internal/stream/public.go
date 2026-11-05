package stream

// PublicEvent removes model-only provenance on a copy. Recorders keep the
// original event, while SSE, WS and exported replay share the public shape.
func PublicEvent(event EventData) EventData {
	if _, ok := event.Payload["mailHeader"]; !ok {
		return event
	}
	payload := make(map[string]any, len(event.Payload))
	for key, value := range event.Payload {
		if key != "mailHeader" {
			payload[key] = value
		}
	}
	event.Payload = payload
	return event
}
