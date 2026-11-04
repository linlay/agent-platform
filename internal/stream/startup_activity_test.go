package stream

import (
	"agent-platform/internal/i18n"
	"testing"
)

func TestStartupActivityOnlyInSimpleBootstrap(t *testing.T) {
	for _, level := range []DetailLevel{DetailSimple, DetailDetailed} {
		a := NewAssembler(StreamRequest{RunID: "random-run-42", ChatID: "chat", Detail: level})
		emissions := a.BootstrapEmissions()
		count := 0
		for i, emission := range emissions {
			e := emission.Event
			if !IsStartupActivity(e.Type, e.Payload) {
				continue
			}
			count++
			if i == 0 || emissions[i-1].Event.Type != "run.start" || !emission.Ephemeral || e.Seq != 0 {
				t.Fatalf("startup order or persistence: %#v", emissions)
			}
			for _, locale := range []string{i18n.LocaleZhCN, i18n.LocaleEN} {
				rendered := i18n.LocalizeEventPayload(locale, e.Type, e.Payload)
				label := i18n.ReasoningLabelForID(locale, "random-run-42")
				want := label + "…"
				if locale == i18n.LocaleZhCN {
					want = "正在" + label
				}
				if rendered["message"] != want || rendered["kind"] != nil || rendered["initial"] != nil || rendered["activity"] != nil {
					t.Fatalf("localized startup: %#v", rendered)
				}
			}
		}
		want := 0
		if level == DetailSimple {
			want = 1
		}
		if count != want {
			t.Fatalf("%s: startup count %d, want %d", level, count, want)
		}
	}
}

func TestSimpleAttachRestoresStartupUntilAnnouncementOrClear(t *testing.T) {
	for _, replacement := range []string{"", ActivityKindAnnounce, "idle"} {
		bus := NewRunEventBus(100, 0, nil)
		initial := NewStartupActivity("run", "chat").Data()
		bus.Publish(initial)
		want := initial.String("message")
		if replacement != "" {
			event := newActivityEvent("run", "chat", "", map[string]any{"kind": replacement, "text": "Next step"}).Data()
			bus.Publish(event)
			want = event.String("message")
		}
		// Hidden automatic activities must not erase simple's startup/announcement.
		bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "tool_run", "toolName": "bash"}).Data())
		observer, err := bus.Subscribe(0)
		if err != nil {
			t.Fatal(err)
		}
		var received []EventData
		for len(observer.Events) > 0 {
			e := <-observer.Events
			if DetailSimple.Allows(e.Type, e.Payload) {
				received = append(received, e)
			}
		}
		if len(received) != 1 || received[0].String("message") != want {
			t.Fatalf("replacement %q: %#v", replacement, received)
		}
		bus.Publish(NewEvent("run.complete", map[string]any{"runId": "run"}).Data())
		terminal, err := bus.Subscribe(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(terminal.Events) != 0 {
			t.Fatal("startup survived run completion")
		}
	}
}

func TestStartupActivityNotRegeneratedForContinuedRun(t *testing.T) {
	a := NewAssembler(StreamRequest{RunID: "run", ChatID: "chat", Detail: DetailSimple, ContinueRun: true})
	for _, emission := range a.BootstrapEmissions() {
		if IsStartupActivity(emission.Event.Type, emission.Event.Payload) {
			t.Fatal("continued run regenerated startup")
		}
	}
}
