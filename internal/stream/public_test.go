package stream

import "testing"

func TestPublicEventHidesHeaderWithoutChangingRecorderInput(t *testing.T) {
	for _, kind := range []string{"request.query", "request.steer"} {
		original := EventData{Type: kind, Payload: map[string]any{"mailId": "m", "mailHeader": "[Letter m]"}}
		public := PublicEvent(original)
		if public.Payload["mailHeader"] != nil || public.Payload["mailId"] != "m" || original.Payload["mailHeader"] != "[Letter m]" {
			t.Fatal(public, original)
		}
	}
}
