package session

import (
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
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "EXECUTOR", "PROXY", "CHANNEL"} {
		for _, engine := range []string{catalog.AgentEngineNative, catalog.AgentEngineACP} {
			if got := PrimaryChatEligible(catalog.AgentDefinition{Mode: mode, Engine: engine}); got != (mode == "GENERAL" && engine == catalog.AgentEngineNative) {
				t.Fatalf("%s/%s: %v", mode, engine, got)
			}
		}
	}
}

type workerRoleHistory struct {
	chat.Store
	content string
}

func (s workerRoleHistory) LoadJSONLContent(string) (string, error) { return s.content, nil }
func (s workerRoleHistory) ChatDir(string) string                   { return "" }

func TestWorkerRoleSurvivesUserInputAndMailboxReset(t *testing.T) {
	for _, tc := range []struct {
		origin string
		marker string
		want   bool
	}{
		{"a", `,"mailNewChat":true`, true},
		{"a", `,"mailNewChat":false`, false},
		{"other", `,"mailNewChat":true`, false},
		{"a", "", true},
	} {
		history := workerRoleHistory{content: `{"_type":"query","lane":"main","taskId":"","origin":{"type":"agent","agentKey":"` + tc.origin + `"}` + tc.marker + `}\n`}
		history.content = strings.ReplaceAll(history.content, `\n`, "\n")
		def := catalog.AgentDefinition{Key: "a", Mode: "GENERAL", Tools: []string{"bash", "chat_send", "chat_submit", "chat_get_status", "chat_interrupt", "chat_notify", "chat_query"}}
		b := New(Dependencies{Chats: history, Config: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}, PrimaryChat: config.PrimaryChatConfig{Enabled: true}}})
		session, err := b.BuildQuerySession(t.Context(), runtimetypes.QueryCommand{ChatID: "worker", AgentKey: "a", Message: "user follow-up"}, chat.Summary{ChatID: "worker"}, def, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if ContainsTool(session.ToolNames, "chat_send") == tc.want || !ContainsTool(session.ToolNames, "chat_notify") {
			t.Fatalf("role=%v tools=%v", tc.want, session.ToolNames)
		}
	}
}

func TestPrimaryChatDoesNotLoadWorkerExecutionContext(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}, PrimaryChat: config.PrimaryChatConfig{Enabled: true, SystemPrompt: "Schedule"}}
	b := New(Dependencies{Config: cfg})
	def := catalog.AgentDefinition{Key: "a", Mode: "GENERAL", Name: "A", ModelKey: "worker", Tools: []string{"bash", "file_read"}, Skills: []string{"missing"}, AgentsPrompt: "worker instructions", SoulPrompt: "soul"}
	req := runtimetypes.QueryCommand{Primary: true, AgentKey: "a", ChatID: "c", RunID: "r", References: []queryinput.Reference{{Type: "skill", ID: "unread"}}}
	session, err := b.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "c"}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode != "GENERAL" || !session.PrimaryChat || !reflect.DeepEqual(session.ToolNames, contracts.PrimaryChatTools()) {
		t.Fatalf("role/tools: %+v", session)
	}
	if session.AgentsPrompt != "" || session.SkillCatalogPrompt != "" || len(session.SkillIDs) != 0 || len(session.ConnectorDirs) != 0 || session.SkillScripts != nil {
		t.Fatal("worker capabilities leaked into primary execution")
	}
	if session.SoulPrompt != "soul" || len(session.RuntimeContext.References) != 1 {
		t.Fatal("identity or forwarding references lost")
	}
	if !reflect.DeepEqual(def.Tools, []string{"bash", "file_read"}) {
		t.Fatal("published definition mutated")
	}
}
