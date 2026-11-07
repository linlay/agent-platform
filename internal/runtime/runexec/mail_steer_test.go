package runexec

import (
	"fmt"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func TestProcessorPersistsConsecutiveMailboxSteers(t *testing.T) {
	for _, kind := range []string{"reply", "ask", "letter", "notify", "user"} {
		t.Run(kind, func(t *testing.T) {
			store, err := chat.NewFileStoreAtStartup(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			const chatID, runID = "main-chat", "main-run"
			if _, _, err := store.EnsureChat(chatID, "agent", "question"); err != nil {
				t.Fatal(err)
			}
			writer := chat.NewStepWriter(store, chatID, runID, "GENERAL")
			processor := NewProcessor(ProcessorOptions{StepWriter: writer})
			dispatcher := stream.NewDispatcher(stream.StreamRequest{ChatID: chatID, RunID: runID})
			for i := 1; i <= 3; i++ {
				mail := &queryinput.MailInput{
					OriginType: "agent", ID: fmt.Sprintf("mail-%d", i), Kind: kind,
					Attempt: i, Hop: 2,
					Header: fmt.Sprintf("[%s mail-%d from agent agent]\n", kind, i),
				}
				origin := &queryinput.Origin{Type: "agent", AgentKey: "agent", ChatID: "work-chat", RunID: "work-run"}
				if kind == "letter" || kind == "notify" {
					origin.ToolID = "send-tool"
				}
				if kind == "reply" {
					mail.Outcome = "completed"
				}
				if kind == "user" {
					mail.OriginType, mail.Kind, mail.Header, mail.Hop = "user", "letter", "", 0
					origin = nil
				}
				body := fmt.Sprintf("result %d", i)
				for _, event := range dispatcher.Dispatch(stream.RequestSteer{
					ChatID: chatID, RunID: runID, SteerID: mail.ID + ":1", Message: body, Origin: origin, Mail: mail,
				}) {
					if _, _, err := processor.Consume(event); err != nil {
						t.Fatalf("processing mail %d: %v", i, err)
					}
				}
			}
			writer.Flush()
			content, err := store.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if err := chat.ValidateJSONLContent(content, "chat.jsonl"); err != nil {
				t.Fatal(err)
			}
			messages, err := store.LoadRawMessages(chatID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 3 {
				t.Fatalf("got %d messages, want all three mails", len(messages))
			}
			for i, message := range messages {
				input, ok := message[chat.SteerHistoryInputKey].(map[string]any)
				if kind == "user" {
					if ok || message["content"] != fmt.Sprintf("result %d", i+1) {
						t.Fatalf("user message gained a mail envelope: %#v", message)
					}
					continue
				}
				if !ok {
					t.Fatalf("mail %d lost trusted history input: %#v", i+1, message)
				}
				mail := queryinput.MailFromPayload(input)
				if mail == nil || mail.ID != fmt.Sprintf("mail-%d", i+1) || mail.Attempt != i+1 {
					t.Fatalf("mail %d lost provenance: %#v", i+1, mail)
				}
				if kind == "reply" && (mail.Outcome != "completed" || !strings.Contains(mail.Header, mail.ID)) {
					t.Fatalf("mail %d lost reply outcome or header: %#v", i+1, mail)
				}
			}
		})
	}
}
