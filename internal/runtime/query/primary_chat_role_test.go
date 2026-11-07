package query

import (
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/runstate"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

type primaryRoleRegistry struct {
	catalog.Registry
	def catalog.AgentDefinition
}

func (r primaryRoleRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return r.def, true
}

type primaryRoleStore struct {
	chat.Store
	chat.PrimaryStore
}
type primaryPointer struct{ chat.PrimaryStore }

func (primaryPointer) PrimaryChatID(string) (string, error) { return "primary", nil }

func TestPrimaryWorkerAccessFrozenAtAtomicAcceptance(t *testing.T) {
	for _, explicit := range []string{"", "default", contracts.AccessLevelFullAccess} {
		t.Run("level="+explicit, func(t *testing.T) {
			def := catalog.AgentDefinition{Key: "a", Mode: "BOT", AccessConfig: contracts.AgentAccessConfig{Default: contracts.AccessLevelAskForApproval}}
			runs := runstate.NewManager()
			runs.Register(t.Context(), contracts.QuerySession{RunID: "r", ChatID: "primary", AgentKey: "a", RunOwner: contracts.AgentRunOwner("a"), AccessLevel: contracts.AccessLevelFullAccess})
			runs.BindRunControlScope("r", controlscope.Scope{Subject: "owner"})
			s := NewService(Dependencies{Config: config.Config{Bot: config.BotConfig{}}, Registry: primaryRoleRegistry{def: def}, Chats: primaryRoleStore{PrimaryStore: primaryPointer{}}, Runs: runs})
			r := contracts.RunStartRequest{AgentKey: "a", Message: "work", AccessLevel: explicit, Origin: contracts.RunOrigin{AgentKey: "a", ChatID: "primary", RunID: "r", ToolID: "t", Subject: "owner"}}
			plan, err := s.PrepareRunStart(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			ack := runs.UpdateAccessLevel(api.AccessLevelRequest{RunID: "r", AgentKey: "a", AccessLevel: contracts.AccessLevelAutoApprove})
			if !ack.Accepted {
				t.Fatal(ack)
			}
			var frozen mailbox.Grant
			err = s.AcceptMail(t.Context(), mailbox.Request{}, mailbox.Prepared{Request: r, OpenedChat: true, Plan: plan}, func(grant mailbox.Grant) error { frozen = grant; return nil })
			if explicit == contracts.AccessLevelFullAccess {
				if err == nil {
					t.Fatal("explicit escalation accepted against stale level")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := contracts.AccessLevelAutoApprove
			if frozen.AccessLevel != want {
				t.Fatalf("frozen=%s want=%s", frozen.AccessLevel, want)
			}
			runs.UpdateAccessLevel(api.AccessLevelRequest{RunID: "r", AgentKey: "a", AccessLevel: contracts.AccessLevelAskForApproval})
			if frozen.AccessLevel != want {
				t.Fatal("accepted grant changed")
			}
		})
	}
}

func TestPrimaryWorkerDefaultSelectorCannotUseHigherAgentDefault(t *testing.T) {
	for _, requested := range []string{"", "default", " DEFAULT "} {
		t.Run("selector="+requested, func(t *testing.T) {
			def := catalog.AgentDefinition{Key: "a", Mode: "BOT", AccessConfig: contracts.AgentAccessConfig{Default: contracts.AccessLevelFullAccess}}
			runs := runstate.NewManager()
			runs.Register(t.Context(), contracts.QuerySession{RunID: "r", ChatID: "primary", AgentKey: "a", RunOwner: contracts.AgentRunOwner("a"), AccessLevel: contracts.AccessLevelAskForApproval})
			runs.BindRunControlScope("r", controlscope.Scope{Subject: "owner"})
			s := NewService(Dependencies{Config: config.Config{Bot: config.BotConfig{}}, Registry: primaryRoleRegistry{def: def}, Chats: primaryRoleStore{PrimaryStore: primaryPointer{}}, Runs: runs})
			r := contracts.RunStartRequest{AgentKey: "a", Message: "work", AccessLevel: requested, Origin: contracts.RunOrigin{AgentKey: "a", ChatID: "primary", RunID: "r", ToolID: "t", Subject: "owner"}}
			plan, err := s.PrepareRunStart(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			var grant mailbox.Grant
			if err := s.AcceptMail(t.Context(), mailbox.Request{}, mailbox.Prepared{Request: r, OpenedChat: true, Plan: plan}, func(g mailbox.Grant) error { grant = g; return nil }); err != nil {
				t.Fatal(err)
			}
			if grant.AccessLevel != contracts.AccessLevelAskForApproval || grant.AccessGrant["source"] != "primary-chat" {
				t.Fatalf("Agent default escalated worker permissions: %+v", grant)
			}
		})
	}
}

type mailRoleSummary struct{ chat.Store }

func (mailRoleSummary) Summary(string) (*chat.Summary, error) {
	return &chat.Summary{ChatID: "primary", AgentKey: "a", Source: "query:owner"}, nil
}

func TestPrimaryQueuePolicyOnlyChangesBot(t *testing.T) {
	for _, mode := range []string{"BOT", "GENERAL", "CODER", "KBASE", "EXECUTOR"} {
		def := catalog.AgentDefinition{Key: "a", Mode: mode}
		runs := runstate.NewManager()
		runs.Register(t.Context(), contracts.QuerySession{RunID: "r", ChatID: "primary", AgentKey: "a", RunOwner: contracts.AgentRunOwner("a")})
		s := NewService(Dependencies{Registry: primaryRoleRegistry{def: def}, Runs: runs, Sessions: sessionbuild.New(sessionbuild.Dependencies{}), Chats: primaryRoleStore{Store: mailRoleSummary{}, PrimaryStore: primaryPointer{}}})
		for _, kind := range []mailbox.Kind{mailbox.Letter, mailbox.Reply, mailbox.Notify, mailbox.Ask} {
			target, err := s.MailTarget(t.Context(), mailbox.Mail{Kind: kind, OriginType: "agent", ToAgentKey: "a", ToChatID: "primary", FromChatID: "worker", Subject: "owner"})
			if err != nil || target.Queue != (mode == "BOT") {
				t.Fatalf("%s kind=%s target=%+v err=%v", mode, kind, target, err)
			}
		}
	}
}

func TestBotMainSteerRequiresQueueButSideAndWorkerDoNot(t *testing.T) {
	for _, tc := range []struct {
		mode, chat, scope string
		blocked           bool
	}{
		{"BOT", "primary", "", true}, {"BOT", "worker", "", false}, {"BOT", "primary", "btw:primary", false}, {"BOT", "primary", "explain:primary", false}, {"GENERAL", "primary", "", false},
	} {
		t.Run(tc.mode+tc.chat+tc.scope, func(t *testing.T) {
			runs := runstate.NewManager()
			runs.Register(t.Context(), contracts.QuerySession{RunID: "r", AgentKey: "a", ChatID: tc.chat, RunScopeID: tc.scope, RunOwner: contracts.AgentRunOwner("a")})
			s := NewService(Dependencies{Registry: primaryRoleRegistry{def: catalog.AgentDefinition{Key: "a", Mode: tc.mode}}, Chats: primaryRoleStore{PrimaryStore: primaryPointer{}}, Runs: runs})
			_, err := s.Steer(t.Context(), runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: "r", ChatID: tc.chat, AgentKey: "a"}, Message: "extra"})
			if tc.blocked {
				e, ok := err.(*statusError)
				if !ok || e.Code != "bot_chat_queue_only" {
					t.Fatalf("queue-only error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
