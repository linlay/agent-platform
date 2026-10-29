package mailbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/sqlitecontract"
	"agent-platform/internal/stream"
)

func TestLegacyMailSchemaRejectedWithoutChangingData(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE mails (id TEXT PRIMARY KEY, message TEXT); INSERT INTO mails VALUES ('old','keep me'); PRAGMA application_id=0x41504D4C; PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("old schema accepted: %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(root, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var text string
	var version int
	if err = db.QueryRow("SELECT message FROM mails WHERE id='old'").Scan(&text); err != nil || text != "keep me" {
		t.Fatal(text, err)
	}
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal(version, err)
	}
}

func TestRetentionClearsBodiesButPreservesReceiptsRelationsAndChatFiles(t *testing.T) {
	s, _ := testBox(t)
	at := time.Now()
	old := at.AddDate(0, 0, -31).UnixMilli()
	letter := sample("old-letter", Letter, Done)
	letter.OpenedChat = true
	letter.AnsweredBy = "old-reply"
	letter.TerminalAt = old
	letter.Input = json.RawMessage(`{"message":"private"}`)
	letter.References = []queryinput.Reference{{Type: "chat", ID: "context"}}
	reply := sample("old-reply", Reply, Delivered)
	reply.ReplyKey = "run:finished:sender"
	reply.TerminalAt = old
	reply.Error = map[string]any{"message": "private"}
	reply.Files = []File{{Name: "report.md", Path: "mail/old-reply/report.md"}}
	ask := sample("old-ask", Ask, Done)
	ask.Message = `{"questions":[{"question":"private"}]}`
	ask.ParentID = letter.ID
	ask.AwaitingID = "a1"
	ask.TerminalAt = old
	suspended := sample("suspended", Letter, Suspended)
	suspended.CreatedAt = old
	recent := sample("recent", Letter, Done)
	recent.CreatedAt = old
	for _, m := range []Mail{letter, reply, ask, suspended, recent} {
		put(t, s.Store, m)
	}
	attachment := filepath.Join(s.root, "receiver", "mail", "old-reply", "report.md")
	if err := os.MkdirAll(filepath.Dir(attachment), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachment, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	// A metadata update must not postpone retention.
	if _, err := s.Store.Change(letter.ID, func(m *Mail) error { m.Outcome = "completed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(at, 30, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{letter.ID, reply.ID, ask.ID} {
		m, err := s.Store.Get(id)
		if err != nil || m.PurgedAt == 0 || m.Message != "" || len(m.Input) != 0 || len(m.References) != 0 || len(m.Files) != 0 || m.Error != nil || m.TerminalAt != old {
			t.Fatalf("not expired correctly: %#v %v", m, err)
		}
	}
	for _, id := range []string{suspended.ID, recent.ID} {
		m, err := s.Store.Get(id)
		if err != nil || m.PurgedAt != 0 || m.Message == "" {
			t.Fatalf("live/recent mail lost: %#v %v", m, err)
		}
	}
	rows, err := s.Store.OpenedChats("lead", "user")
	if err != nil || len(rows) != 1 || rows[0].ID != letter.ID {
		t.Fatalf("opening relationship lost: %#v %v", rows, err)
	}
	if !strings.Contains(s.Header(reply.ID), letter.ID) || !strings.Contains(s.Header(ask.ID), letter.ID) {
		t.Fatal("envelope relations disappeared with body")
	}
	replay, inserted, err := s.Store.Accept(letter)
	if err != nil || inserted || replay.ID != letter.ID || replay.PurgedAt == 0 {
		t.Fatalf("idempotency lost: %#v %v", replay, err)
	}
	if body, err := os.ReadFile(attachment); err != nil || string(body) != "report" {
		t.Fatal("Chat replay resource removed", err)
	}
}

func TestDeletedWorkerReplyDeliversFromReceivingCopy(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	for _, id := range []string{"receiver", "sender"} {
		if err := os.MkdirAll(filepath.Join(s.root, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.root, "receiver", "report.md"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	letter := sample("opening", Letter, Delivered)
	letter.Delivery = "started"
	letter.RunID = "child"
	put(t, s.Store, letter)
	s.setScope(letter.ID, controlscope.Scope{Subject: "user"})
	s.Bind(&artifactsPort{testPort: p, items: []chat.ArtifactManifestItem{{RunID: "child", ArtifactItemState: chat.ArtifactItemState{Name: "report.md", URL: "@chat/report.md", SizeBytes: 3, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}}})
	if err := s.Completed(chat.RunCompletion{RunID: "child", ChatID: "receiver", AssistantText: "finished report", FinishReason: "complete"}, 1); err != nil {
		t.Fatal(err)
	}
	original, _ := s.Store.Get(letter.ID)
	reply, err := s.Store.Get(original.AnsweredBy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeChat("receiver"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.root, "receiver")); err != nil {
		t.Fatal(err)
	}
	reply, err = s.Store.Get(reply.ID)
	if err != nil || reply.Status != Queued || reply.PurgedAt != 0 || reply.FromChatDeletedAt == 0 || reply.Message != "finished report" || len(reply.Files) != 1 {
		t.Fatalf("reply lost: %#v %v", reply, err)
	}
	p.mu.Lock()
	p.target = Target{RunID: "parent"}
	p.callback = func(m Mail) (DeliveryResult, error) {
		return DeliveryResult{Accepted: true, RunID: m.RunID}, s.Consumed(m.Metadata(), m.RunID)
	}
	p.mu.Unlock()
	s.Kick("sender")
	await(t, func() bool { m, _ := s.Store.Get(reply.ID); return m.Status == Delivered })
	if err := s.PurgeChat("sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Get(reply.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("both-deleted receipt retained", err)
	}
}

type generatedAccessPort struct {
	*testPort
	level               string
	failCopy            bool
	persistedBeforeCopy bool
	box                 *Service
}

func (p *generatedAccessPort) MailDefaultAccess(agent string) (Grant, error) {
	return Grant{AccessLevel: p.level, AccessGrant: map[string]any{"source": "agent_default", "agentKey": agent, "agentRevision": "v1"}}, nil
}
func (p *generatedAccessPort) EnsureMailChat(ctx context.Context, m *Mail) error {
	row, err := p.box.Store.Get(m.ID)
	p.persistedBeforeCopy = err == nil && row.Status == Accepting && len(row.Files) == 1
	if p.failCopy {
		return errors.New("temporary disk failure")
	}
	return p.testPort.EnsureMailChat(ctx, m)
}
func TestGeneratedMailFreezesAccessAndNotifyPersistsBeforeCopy(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	for _, id := range []string{"sender", "receiver"} {
		if err := os.MkdirAll(filepath.Join(s.root, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.root, "receiver", "draft.md"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := sample("parent", Letter, Delivered)
	parent.Delivery = "started"
	parent.RunID = "child"
	put(t, s.Store, parent)
	s.setScope(parent.ID, controlscope.Scope{Subject: "user"})
	gp := &generatedAccessPort{testPort: p, level: "full_access", box: s}
	s.Bind(gp)
	origin := contracts.RunOrigin{RunID: "child", ChatID: "receiver", AgentKey: "worker", ToolID: "notify", Subject: "user"}
	receipt, err := s.Notify(context.Background(), "draft", []string{"draft.md"}, origin, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !gp.persistedBeforeCopy {
		t.Fatal("notify copied before durable accepting row")
	}
	notice, err := s.Store.Get(receipt.MailID)
	if err != nil || notice.AccessLevel != "full_access" || notice.AccessGrant["agentKey"] != "lead" || notice.Status != Queued {
		t.Fatalf("notice: %#v %v", notice, err)
	}
	gp.level = "default"
	replay, err := s.Notify(context.Background(), "draft", []string{"draft.md"}, origin, 1)
	if err != nil || replay.MailID != receipt.MailID {
		t.Fatal(replay, err)
	}
	notice, _ = s.Store.Get(receipt.MailID)
	if notice.AccessLevel != "full_access" {
		t.Fatal("grant recomputed on replay")
	}
	gp.level = "auto_approve"
	s.ObserveEvent("child", stream.EventData{Type: "awaiting.ask", Payload: map[string]any{"mode": "question", "awaitingId": "question", "questions": []any{map[string]any{"question": "continue?"}}}})
	asks, err := s.Store.List(Filter{Kind: Ask, Subject: "user"})
	if err != nil || len(asks) != 1 || asks[0].AccessLevel != "auto_approve" {
		t.Fatalf("ask: %#v %v", asks, err)
	}
	if err := s.Completed(chat.RunCompletion{RunID: "child", AssistantText: "done", FinishReason: "complete"}, 1); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Store.Get(parent.ID)
	reply, err := s.Store.Get(current.AnsweredBy)
	if err != nil || reply.AccessLevel != "auto_approve" || reply.AccessGrant["agentRevision"] != "v1" {
		t.Fatalf("reply: %#v %v", reply, err)
	}
}

func TestNotifyCopyFailureKeepsDurableInvocationReceipt(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	if err := os.MkdirAll(filepath.Join(s.root, "receiver"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "receiver", "draft"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := sample("parent", Letter, Delivered)
	parent.Delivery = "started"
	parent.RunID = "child"
	put(t, s.Store, parent)
	s.setScope(parent.ID, controlscope.Scope{Subject: "user"})
	gp := &generatedAccessPort{testPort: p, level: "default", failCopy: true, box: s}
	s.Bind(gp)
	origin := contracts.RunOrigin{RunID: "child", ChatID: "receiver", AgentKey: "worker", ToolID: "notify", Subject: "user"}
	receipt, err := s.Notify(context.Background(), "draft", []string{"draft"}, origin, 1)
	if err != nil || receipt.Status != Withdrawn || !gp.persistedBeforeCopy {
		t.Fatal(receipt, err)
	}
	again, err := s.Notify(context.Background(), "draft", []string{"draft"}, origin, 1)
	if err != nil || again.MailID != receipt.MailID {
		t.Fatal(again, err)
	}
}

func TestRetentionReconcilesMissingChatsButKeepsArchivesAndReservations(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	archived := sample("archived", Reply, Suspended)
	archived.FromChatID = "worker"
	archived.ToChatID = "archived"
	put(t, s.Store, archived)
	reserved := sample("reserved", Letter, Suspended)
	reserved.OpenedChat = true
	reserved.ResumePhase = Accepting
	reserved.ToChatID = "reserved-chat"
	put(t, s.Store, reserved)
	orphan := sample("orphan", Letter, Done)
	orphan.FromChatID = "gone1"
	orphan.ToChatID = "gone2"
	put(t, s.Store, orphan)
	if err := s.Maintain(time.Now(), 30, func(id string) (bool, error) { return id == "worker" || id == "archived" || id == "sender", nil }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{archived.ID, reserved.ID} {
		m, err := s.Store.Get(id)
		if err != nil || m.PurgedAt != 0 || m.Status != Suspended {
			t.Fatal(m, err)
		}
	}
	if _, err := s.Store.Get(orphan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan retained", err)
	}
}

func TestRetentionStartsImmediatelyAndCloseStopsWorker(t *testing.T) {
	s, _ := testBox(t)
	m := sample("expired", Letter, Done)
	m.TerminalAt = time.Now().AddDate(0, 0, -31).UnixMilli()
	put(t, s.Store, m)
	s.StartRetention(30, nil)
	await(t, func() bool { m, err := s.Store.Get(m.ID); return err == nil && m.PurgedAt != 0 })
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retention worker prevented shutdown")
	}
}

func TestStaleCompletionCannotCreateReplyAfterSenderDeletion(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	letter := sample("stale", Letter, Delivered)
	letter.RunID = "worker-run"
	letter.Delivery = "started"
	put(t, s.Store, letter)
	// A completion captured this letter before deletion committed.
	reply := s.newReply(letter, "run:worker-run:sender", "worker-run", "late result", "completed")
	if err := s.PurgeChat("sender"); err != nil {
		t.Fatal(err)
	}
	inserted, settled, err := s.Store.Complete([]Mail{reply}, []Mail{letter}, false)
	if err != nil || len(inserted) != 0 || len(settled) != 0 {
		t.Fatalf("stale completion produced orphan: %#v %#v %v", inserted, settled, err)
	}
}
