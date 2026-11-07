package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestPrimaryChatEligibilityKeepsOtherModesUnchanged(t *testing.T) {
	for _, mode := range []string{"BOT", "GENERAL", "CODER", "KBASE", "TEAM", "EXECUTOR", "PROXY", "CHANNEL"} {
		for _, engine := range []string{catalog.AgentEngineNative, catalog.AgentEngineACP} {
			if got := PrimaryChatEligible(catalog.AgentDefinition{Mode: mode, Engine: engine}); got != (mode == "BOT" && engine == catalog.AgentEngineNative) {
				t.Fatalf("%s/%s: %v", mode, engine, got)
			}
		}
	}
}

type workerRoleHistory struct{ chat.Store }

func (workerRoleHistory) LoadJSONLContent(string) (string, error) {
	return "", errors.New("worker identity must not read JSONL")
}
func (workerRoleHistory) ChatDir(string) string { return "" }

func TestWorkerRoleUsesModeWithoutHistoryRead(t *testing.T) {
	for _, mode := range []string{"BOT", "GENERAL"} {
		t.Run(mode, func(t *testing.T) {
			def := catalog.AgentDefinition{Key: "a", Mode: mode, Tools: []string{"bash", "chat_send", "chat_submit", "chat_get_status", "chat_interrupt", "chat_notify", "chat_query"}}
			b := New(Dependencies{Chats: workerRoleHistory{}, Config: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}})
			session, err := b.BuildQuerySession(t.Context(), runtimetypes.QueryCommand{ChatID: "worker", AgentKey: "a", Message: "user follow-up"}, chat.Summary{ChatID: "worker"}, def, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"chat_send", "chat_submit", "chat_get_status", "chat_interrupt"} {
				if ContainsTool(session.ToolNames, tool) != (mode != "BOT") {
					t.Fatalf("mode=%s tools=%v", mode, session.ToolNames)
				}
			}
			if !ContainsTool(session.ToolNames, "chat_notify") {
				t.Fatal("notification tool lost")
			}
		})
	}
}

func TestPrimaryChatDoesNotLoadWorkerExecutionContext(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}, Bot: config.BotConfig{SystemPrompt: "Schedule"}}
	b := New(Dependencies{Config: cfg})
	def := catalog.AgentDefinition{Key: "a", Mode: "BOT", Name: "A", ModelKey: "worker", Tools: []string{"bash", "file_read"}, Skills: []string{"missing"}, AgentsPrompt: "worker instructions", SoulPrompt: "soul"}
	req := runtimetypes.QueryCommand{Primary: true, AgentKey: "a", ChatID: "c", RunID: "r", References: []queryinput.Reference{{Type: "skill", ID: "unread"}}}
	session, err := b.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "c"}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != "BOT" || !session.PrimaryChat || !reflect.DeepEqual(session.ToolNames, contracts.PrimaryChatTools()) {
		t.Fatalf("role/tools: %+v", session)
	}
	if session.AgentsPrompt != "" || session.SkillCatalogPrompt != "" || len(session.SkillIDs) != 0 || len(session.ConnectorDirs) != 0 || session.SkillScripts != nil {
		t.Fatal("worker capabilities leaked into primary execution")
	}
	if session.SoulPrompt != "soul" || len(session.RuntimeContext.References) != 1 {
		t.Fatal("identity or forwarding references lost")
	}
	for _, required := range []string{"chat_submit", "approval_requires_human", "mail_hop_limit", "files:[path]", "chat_query", "accessLevel:\"default\""} {
		if !strings.Contains(session.PrimaryChatProtocol, required) {
			t.Fatalf("missing scheduling protocol %q", required)
		}
	}
	if !reflect.DeepEqual(def.Tools, []string{"bash", "file_read"}) {
		t.Fatal("published definition mutated")
	}
}

func TestPrimarySideLanesKeepAgentExecutionContext(t *testing.T) {
	for _, lane := range []string{"btw", "explain"} {
		t.Run(lane, func(t *testing.T) {
			b := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}, Bot: config.BotConfig{ModelKey: "scheduler", SystemPrompt: "dispatch"}}})
			def := catalog.AgentDefinition{Key: "a", Mode: "BOT", ModelKey: "worker", AgentsPrompt: "agent-side-rules", Tools: []string{"file_read", "chat_send"}}
			session, err := b.BuildQuerySession(t.Context(), runtimetypes.QueryCommand{Primary: true, Lane: lane, ChatID: "c", AgentKey: "a"}, chat.Summary{ChatID: "c"}, def, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if session.PrimaryChat || session.PrimaryChatProtocol != "" || session.ModelKey != "worker" || session.AgentsPrompt != "agent-side-rules" || !ContainsTool(session.ToolNames, "file_read") {
				t.Fatalf("side lane inherited scheduling context: %+v", session)
			}
		})
	}
}
