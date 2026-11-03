package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/stream"
)

type testPort struct {
	mu         sync.Mutex
	root       string
	target     Target
	targetErr  error
	deliveries []Mail
	callback   func(Mail) (DeliveryResult, error)
}

func (p *testPort) MailDefaultAccess(agentKey string) (Grant, error) {
	return Grant{AccessLevel: "ask_for_approval", AccessGrant: map[string]any{"source": "default", "agentKey": agentKey, "agentRevision": "test"}}, nil
}

func (p *testPort) PrepareMail(_ context.Context, r Request) (Prepared, error) {
	r.Start.AgentKey = "worker"
	return Prepared{Request: r.Start, Plan: contracts.RunStartPlan{AccessLevel: "ask_for_approval"}, ChatID: "receiver", OpenedChat: true}, nil
}
func (p *testPort) AcceptMail(_ context.Context, _ Request, _ Prepared, save func(Grant) error) error {
	return save(Grant{AccessLevel: "ask_for_approval", Scope: controlscope.Scope{Transport: "ws", Subject: "user", Lane: "main"}})
}
func (p *testPort) EnsureMailChat(_ context.Context, m *Mail) error {
	return os.MkdirAll(filepath.Join(p.root, m.ToChatID), 0755)
}
func (p *testPort) MailTarget(context.Context, Mail) (Target, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target, p.targetErr
}
func (p *testPort) DeliverMail(_ context.Context, m Mail, _ controlscope.Scope) (DeliveryResult, error) {
	p.mu.Lock()
	p.deliveries = append(p.deliveries, m)
	cb := p.callback
	p.mu.Unlock()
	if cb != nil {
		return cb(m)
	}
	return DeliveryResult{Accepted: true, RunID: m.RunID}, nil
}
func (p *testPort) SubmitMail(context.Context, Mail, queryinput.SubmitRequest, *contracts.ExecutionContext) (queryinput.SubmitResponse, error) {
	return queryinput.SubmitResponse{Accepted: true}, nil
}
func testBox(t *testing.T) (*Service, *testPort) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, store, root, 4, nil)
	p := &testPort{root: root, target: Target{RunID: "target-run"}}
	s.Bind(p)
	t.Cleanup(func() {
		cancel()
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, p
}
func sample(id string, kind Kind, status Status) Mail {
	return Mail{ID: id, Kind: kind, Status: status, CreatedAt: now(), UpdatedAt: now(), ToAgentKey: "worker", ToChatID: "receiver", OriginType: "agent", FromAgentKey: "lead", FromChatID: "sender", FromRunID: "parent-run", FromToolID: id, Subject: "user", Message: "work", Hop: 1, AccessLevel: "ask_for_approval", RequestDigest: id}
}
func put(t *testing.T, s *Store, m Mail) {
	t.Helper()
	if _, _, err := s.Accept(m); err != nil {
		t.Fatal(err)
	}
}
func await(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestStoreIdempotencySurvivesReopenAndPurging(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	m := sample("m1", Letter, Queued)
	put(t, s, m)
	_, err = s.Change(m.ID, func(r *Mail) error { r.Message = ""; r.Input = nil; r.PurgedAt = now(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m.ID = "other"
	old, created, err := s.Accept(m)
	if err != nil || created || old.ID != "m1" || old.Message != "" {
		t.Fatalf("replay: %#v %v %v", old, created, err)
	}
	m.RequestDigest = "different"
	if _, _, err = s.Accept(m); err == nil {
		t.Fatal("changed invocation accepted")
	}
}
func TestInflightIsReservedBeforeDeliveryAndNotEnqueuedTwice(t *testing.T) {
	s, p := testBox(t)
	m := sample("m1", Letter, Queued)
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	p.callback = func(sent Mail) (DeliveryResult, error) {
		row, err := s.Store.Get(sent.ID)
		if err != nil || row.Status != Inflight || row.Attempts != 1 || row.RunID != sent.RunID {
			t.Errorf("not reserved before IO: %#v %v", row, err)
		}
		return DeliveryResult{Accepted: true}, nil
	}
	for i := 0; i < 20; i++ {
		s.Kick("receiver")
	}
	await(t, func() bool { row, _ := s.Store.Get(m.ID); return row.Status == Inflight })
	s.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.deliveries) != 1 {
		t.Fatalf("got %d sends", len(p.deliveries))
	}
}
func TestConsumeBeforeDeliveryReturnsAndStaleAttemptFencing(t *testing.T) {
	s, p := testBox(t)
	m := sample("m1", Letter, Queued)
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	p.callback = func(sent Mail) (DeliveryResult, error) {
		if err := s.Consumed(sent.Metadata(), sent.RunID); err != nil {
			t.Error(err)
		}
		return DeliveryResult{Accepted: true}, nil
	}
	s.Kick("receiver")
	await(t, func() bool { row, _ := s.Store.Get(m.ID); return row.Status == Delivered })
	row, err := s.Store.Change(m.ID, func(r *Mail) error { r.Status = Inflight; r.Attempts++; r.RunID = "new-run"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Consumed(&queryinput.MailInput{ID: m.ID, Attempt: row.Attempts - 1}, "target-run"); err != nil {
		t.Fatal(err)
	}
	row, _ = s.Store.Get(m.ID)
	if row.Status != Inflight {
		t.Fatal("old callback consumed new attempt")
	}
}
func TestRestartRetainsAcceptancePhaseAndFinalizesConsumedReply(t *testing.T) {
	s, _ := testBox(t)
	for _, m := range []Mail{sample("accept", Letter, Accepting), sample("queue", Letter, Queued), sample("letter", Letter, Delivered), sample("reply", Reply, Inflight), sample("ask", Ask, Delivered)} {
		if m.Kind == Reply {
			m.FromToolID = ""
			m.ReplyKey = "run:x:sender"
		}
		if m.Kind == Ask {
			m.FromToolID = ""
			m.AwaitingID = "a1"
		}
		put(t, s.Store, m)
	}
	if err := s.Reconcile(func(m Mail) (bool, error) { return m.ID == "reply", nil }); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Store.Get("accept")
	q, _ := s.Store.Get("queue")
	r, _ := s.Store.Get("reply")
	l, _ := s.Store.Get("letter")
	ask, _ := s.Store.Get("ask")
	if a.ResumePhase != Accepting || q.ResumePhase != Queued || a.Status != Suspended || q.Evidence != "none" {
		t.Fatalf("phases lost: %#v %#v", a, q)
	}
	if r.Status != Delivered || l.Status != Suspended || l.Evidence != "consumed" || ask.Status != Withdrawn {
		t.Fatalf("bad recovery: reply=%s letter=%s/%s ask=%s", r.Status, l.Status, l.Evidence, ask.Status)
	}
}
func TestCompleteGroupsLettersAndIsDurablyIdempotent(t *testing.T) {
	s, _ := testBox(t)
	for _, id := range []string{"m1", "m2"} {
		m := sample(id, Letter, Delivered)
		m.RunID = "work-run"
		m.Delivery = "inserted"
		put(t, s.Store, m)
	}
	completion := chat.RunCompletion{RunID: "work-run", ChatID: "receiver", AgentKey: "worker", FinishReason: "complete", AssistantText: strings.Repeat("result", 100)}
	if err := s.Completed(completion, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Completed(completion, 2); err != nil {
		t.Fatal(err)
	}
	rows, err := s.all(Filter{Kind: Reply, Subject: "user"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("replies=%d err=%v", len(rows), err)
	}
	for _, id := range []string{"m1", "m2"} {
		m, _ := s.Store.Get(id)
		if m.Status != Done || m.AnsweredBy != rows[0].ID {
			t.Fatalf("not answered: %#v", m)
		}
	}
	if rows[0].Message != completion.AssistantText || rows[0].Hop != 3 {
		t.Fatal("reply did not retain full result and causal hop")
	}
}
func TestAdmissionFailuresDoNotCollideWithoutRun(t *testing.T) {
	s, _ := testBox(t)
	for _, id := range []string{"m1", "m2"} {
		m := sample(id, Letter, Queued)
		put(t, s.Store, m)
		s.fail(m, failure("agent_not_found", "removed"))
	}
	rows, err := s.all(Filter{Kind: Reply, Subject: "user"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("replies=%d err=%v", len(rows), err)
	}
	if rows[0].ReplyKey == rows[1].ReplyKey {
		t.Fatal("failure reply keys collided")
	}
}
func TestAttachmentSnapshotAndTraversal(t *testing.T) {
	s, _ := testBox(t)
	if err := os.MkdirAll(filepath.Join(s.root, "sender"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "sender", "a.txt"), []byte("accepted"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := s.prepareFiles("sender", "m1", []string{"@chat/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	m := sample("m1", Letter, Accepting)
	m.Files = files
	if err = s.copyFiles(m); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.root, "sender", "a.txt"), []byte("changed"), 0600)
	if err = s.copyFiles(m); err != nil {
		t.Fatal("immutable destination should survive changed source", err)
	}
	if _, err = s.prepareFiles("sender", "m2", []string{"../mail.db"}); err == nil {
		t.Fatal("traversal accepted")
	}
	if err = os.Symlink(filepath.Join(s.root, "mail.db"), filepath.Join(s.root, "sender", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareFiles("sender", "m2", []string{"escape"}); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
func TestStrictDatabaseIdentityIsNotAdopted(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("PRAGMA application_id=0")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open(root); err == nil {
		t.Fatal("unmarked mailbox adopted")
	}
}
func TestMailStatusDoesNotGrantInsertedRunControl(t *testing.T) {
	s, _ := testBox(t)
	m := sample("m1", Letter, Delivered)
	m.RunID = "shared-run"
	m.Delivery = "inserted"
	put(t, s.Store, m)
	if s.OwnsRun(m.RunID, m.FromAgentKey, m.Subject) {
		t.Fatal("insertion granted Run control")
	}
	if _, err := s.Owned(m.ID, m.FromAgentKey, "other-user"); err == nil {
		t.Fatal("cross-principal mail access")
	}
}
func TestMailInputMetadataCannotBeForgedByPublicSteer(t *testing.T) {
	var req queryinput.SteerRequest
	if err := json.Unmarshal([]byte(`{"runId":"r","message":"x","mailId":"forged"}`), &req); err == nil {
		t.Fatal("public steer accepted mail provenance")
	}
}

func TestNotifyAndAskFollowOpeningLetterAndWithdraw(t *testing.T) {
	s, p := testBox(t)
	parent := sample("opening", Letter, Delivered)
	parent.Delivery = "started"
	parent.RunID = "child"
	parent.FromToolID = "send"
	put(t, s.Store, parent)
	s.setScope(parent.ID, controlscope.Scope{Transport: "http", Subject: "user"})
	p.mu.Lock()
	p.target = Target{Queue: true}
	p.mu.Unlock()
	notice, err := s.Notify(context.Background(), "draft ready", nil, contracts.RunOrigin{RunID: "child", ChatID: "receiver", AgentKey: "worker", ToolID: "notify", Subject: "user"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := s.Store.Get(notice.MailID)
	if n.Kind != Notify || n.ParentID != parent.ID || n.ToChatID != "sender" || n.Hop != 2 {
		t.Fatalf("notify: %#v", n)
	}
	event := stream.EventData{Type: "awaiting.ask", Payload: map[string]any{"mode": "form", "awaitingId": "form", "view": map[string]any{"source": "builtin", "key": "ask_user_form"}, "form": map[string]any{"data": map[string]any{"html": `<select name="region"><option value="cn">China</option></select>`}}}}
	s.ObserveEvent("child", event)
	s.ObserveEvent("child", event)
	asks, err := s.Store.List(Filter{Kind: Ask, Subject: "user"})
	if err != nil || len(asks) != 1 {
		t.Fatalf("asks: %#v %v", asks, err)
	}
	if asks[0].AwaitingSource != "ask_user_form" || !strings.Contains(asks[0].Message, `"fields":[{"name":"region","type":"select","options":["cn"]}]`) {
		t.Fatalf("form snapshot: %#v", asks[0])
	}
	s.ObserveEvent("child", stream.EventData{Type: "awaiting.answer", Payload: map[string]any{"awaitingId": "form"}})
	a, _ := s.Store.Get(asks[0].ID)
	if a.Status != Withdrawn {
		t.Fatalf("resolved queued ask: %#v", a)
	}
	if _, err := s.Submit(context.Background(), queryinput.SubmitRequest{RunID: "child", AwaitingID: "form"}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "sender", Subject: "user"}}); err == nil {
		t.Fatal("resolved ask accepted")
	}
}

func TestAttachmentDestinationSymlinkDoesNotCreateOutsideDirectory(t *testing.T) {
	s, _ := testBox(t)
	for _, id := range []string{"sender", "receiver"} {
		if err := os.MkdirAll(filepath.Join(s.root, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.root, "sender", "x.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.root, "receiver", "mail")); err != nil {
		t.Skip(err)
	}
	files, err := s.prepareFiles("sender", "mail_copy", []string{"x.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.copyFiles(Mail{ID: "mail_copy", FromChatID: "sender", ToChatID: "receiver", Files: files}); err == nil {
		t.Fatal("symlink copy accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("copy created directories outside Chat: %#v %v", entries, err)
	}
}

type artifactsPort struct {
	*testPort
	items []chat.ArtifactManifestItem
}

func (p *artifactsPort) PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error) {
	return p.items, nil
}
func TestReplyAttachmentFailurePreservesResultForManualRecovery(t *testing.T) {
	s, p := testBox(t)
	m := sample("letter-attachment", Letter, Delivered)
	m.RunID = "finished-child"
	m.Delivery = "started"
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{Transport: "http", Subject: "user"})
	for _, id := range []string{"sender", "receiver"} {
		if err := os.MkdirAll(filepath.Join(s.root, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	s.Bind(&artifactsPort{testPort: p, items: []chat.ArtifactManifestItem{{RunID: m.RunID, ArtifactItemState: chat.ArtifactItemState{Name: "report.md", URL: "@chat/report.md", SizeBytes: 3, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}}})
	if err := s.Completed(chat.RunCompletion{RunID: m.RunID, ChatID: "receiver", AssistantText: "complete report", FinishReason: "complete"}, 1); err != nil {
		t.Fatal(err)
	}
	original, _ := s.Store.Get(m.ID)
	reply, err := s.Store.Get(original.AnsweredBy)
	if err != nil || original.Status != Done || reply.Status != Suspended || reply.ResumePhase != Accepting || reply.Message != "complete report" {
		t.Fatalf("lost result: %#v %#v %v", original, reply, err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "receiver", "report.md"), []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.target = Target{Queue: true}
	p.mu.Unlock()
	recovered, err := s.Action(context.Background(), reply.ID, "redeliver", "user", controlscope.Scope{Transport: "ws", Subject: "user", Boundary: "human"})
	if err != nil || recovered.Status != Queued {
		t.Fatalf("recovery: %#v %v", recovered, err)
	}
	body, err := os.ReadFile(filepath.Join(s.root, "sender", filepath.FromSlash(reply.Files[0].Path)))
	if err != nil || string(body) != "abc" {
		t.Fatalf("recovered artifact %q %v", body, err)
	}
}

func TestDurableReceiptReplayIgnoresLaterHopAndParentTermination(t *testing.T) {
	s, p := testBox(t)
	p.mu.Lock()
	p.target = Target{Queue: true}
	p.mu.Unlock()
	request := Request{Start: contracts.RunStartRequest{Origin: contracts.RunOrigin{RunID: "sender-run", ToolID: "send", ChatID: "sender", AgentKey: "lead", Subject: "user"}, Message: "work", Input: map[string]any{"message": "work", "newChat": true}}, NewChat: true}
	first, err := s.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Hop = 4
	replay, err := s.Send(context.Background(), request)
	if err != nil || replay.MailID != first.MailID {
		t.Fatalf("receipt lost after hop increased: %#v %v", replay, err)
	}
	parent := sample("parent-letter", Letter, Delivered)
	parent.RunID = "child"
	parent.Delivery = "started"
	put(t, s.Store, parent)
	s.setScope(parent.ID, controlscope.Scope{})
	origin := contracts.RunOrigin{RunID: "child", ToolID: "notify", ChatID: "receiver", AgentKey: "worker", Subject: "user"}
	notice, err := s.Notify(context.Background(), "draft", nil, origin, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Change(parent.ID, func(row *Mail) error { row.Status = Done; return nil }); err != nil {
		t.Fatal(err)
	}
	replay, err = s.Notify(context.Background(), "draft", nil, origin, 4)
	if err != nil || replay.MailID != notice.MailID {
		t.Fatalf("notify receipt lost after parent completion: %#v %v", replay, err)
	}
}

func TestConcurrentAcceptanceReplayNeverFailsAnAcceptedLetter(t *testing.T) {
	s, p := testBox(t)
	p.mu.Lock()
	p.target = Target{Queue: true}
	p.mu.Unlock()
	req := Request{Start: contracts.RunStartRequest{Origin: contracts.RunOrigin{RunID: "r", ToolID: "send", ChatID: "sender", AgentKey: "lead", Subject: "user"}, Message: "work", Input: map[string]any{"message": "work"}}}
	var wg sync.WaitGroup
	results := make(chan Receipt, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Send(context.Background(), req)
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	id := ""
	for r := range results {
		if id == "" {
			id = r.MailID
		}
		if r.MailID != id || r.Status == Failed {
			t.Fatalf("conflicting acceptance receipt: %#v", r)
		}
	}
	rows, err := s.Store.List(Filter{Kind: Letter, Subject: "user"})
	if err != nil || len(rows) != 1 || rows[0].Status != Queued {
		t.Fatalf("replayed letter %#v %v", rows, err)
	}
}

func TestAcceptedFailureStillReturnsDurableReceipt(t *testing.T) {
	s, p := testBox(t)
	p.mu.Lock()
	p.target = Target{Queue: true}
	p.mu.Unlock()
	broken := filepath.Join(s.root, "not-a-directory")
	if err := os.WriteFile(broken, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	p.root = broken
	req := Request{Start: contracts.RunStartRequest{Origin: contracts.RunOrigin{RunID: "r", ToolID: "send", ChatID: "sender", AgentKey: "lead", Subject: "user"}, Message: "work", Input: map[string]any{"message": "work"}}}
	result, err := s.Send(context.Background(), req)
	if err != nil || result.MailID == "" || result.Status != Failed || result.Error == nil {
		t.Fatalf("accepted failure lost receipt: %#v %v", result, err)
	}
	replay, err := s.Send(context.Background(), req)
	if err != nil || replay.MailID != result.MailID {
		t.Fatalf("failed receipt was not durable: %#v %v", replay, err)
	}
}

func TestRunControlComesOnlyFromStartedLetters(t *testing.T) {
	s, _ := testBox(t)
	letter := sample("letter", Letter, Delivered)
	letter.Delivery, letter.RunID = "started", "worker-run"
	put(t, s.Store, letter)
	reply := sample("reply", Reply, Delivered)
	reply.FromAgentKey, reply.FromToolID, reply.ReplyKey = "worker", "", "run:x:sender"
	reply.Delivery, reply.RunID = "started", "lead-run"
	put(t, s.Store, reply)
	if !s.OwnsRun("worker-run", "lead", "user") {
		t.Fatal("letter sender lost control of the Run its letter opened")
	}
	if s.OwnsRun("lead-run", "worker", "user") || s.OwnsRun("worker-run", "lead", "other") {
		t.Fatal("a reply sender or another principal gained Run control")
	}
}

func TestTransientTargetErrorKeepsQueueAndRejectFails(t *testing.T) {
	s, p := testBox(t)
	m := sample("m1", Letter, Queued)
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	p.targetErr = errors.New("summary read failed")
	s.Kick(m.ToChatID)
	s.wg.Wait()
	if got, _ := s.Store.Get(m.ID); got.Status != Queued {
		t.Fatalf("transient error changed the letter: %s", got.Status)
	}
	p.mu.Lock()
	p.targetErr = Reject(failure("agent_not_found", "gone"))
	p.mu.Unlock()
	s.Kick(m.ToChatID)
	s.wg.Wait()
	got, _ := s.Store.Get(m.ID)
	if got.Status != Failed || got.AnsweredBy == "" {
		t.Fatalf("definitive rejection: %#v", got)
	}
	if reply, err := s.Store.Get(got.AnsweredBy); err != nil || reply.ReplyKey != "letter:"+m.ID || reply.Outcome != "failed" {
		t.Fatalf("failure reply: %#v %v", reply, err)
	}
}

func TestStartedDeliveryUsesPlatformRunIDFormat(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{}
	m := sample("m1", Letter, Queued)
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	s.Kick(m.ToChatID)
	s.wg.Wait()
	got, _ := s.Store.Get(m.ID)
	if got.Delivery != "started" {
		t.Fatalf("delivery = %q", got.Delivery)
	}
	if _, err := strconv.ParseInt(got.RunID, 36, 64); err != nil {
		t.Fatalf("run ID %q is not a base36 epoch-millisecond ID", got.RunID)
	}
	s.mu.Lock()
	locks := len(s.chatLocks)
	s.mu.Unlock()
	if locks != 0 {
		t.Fatalf("idle Chat kept %d dispatch locks", locks)
	}
}

func TestStartedMailTheRunNeverReadFailsInsteadOfLooping(t *testing.T) {
	s, _ := testBox(t)
	m := sample("m1", Letter, Inflight)
	m.Delivery, m.RunID, m.Attempts = "started", "dead-run", 1
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	if err := s.Completed(chat.RunCompletion{RunID: "dead-run", FinishReason: "error"}, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Store.Get(m.ID); got.Status != Failed {
		t.Fatalf("unread started letter was %s", got.Status)
	}
	if _, ok := s.scope(m.ID); ok {
		t.Fatal("a failed letter kept its delivery identity")
	}
}

func TestQueuedMailWithoutIdentityIsSuspendedAsUndelivered(t *testing.T) {
	s, _ := testBox(t)
	m := sample("m1", Letter, Queued)
	put(t, s.Store, m)
	s.Kick(m.ToChatID)
	s.wg.Wait()
	got, _ := s.Store.Get(m.ID)
	if got.Status != Suspended || got.Evidence != "none" || got.ResumePhase != Queued {
		t.Fatalf("got %s/%s/%s", got.Status, got.Evidence, got.ResumePhase)
	}
}

func TestReplyOutcomeCoversEveryFinishSpelling(t *testing.T) {
	for reason, want := range map[string]string{"complete": "completed", "stop": "completed", "cancel": "canceled", "cancelled": "canceled", "interrupted": "canceled", "error": "failed", "failed": "failed", "timeout": "failed"} {
		if got := outcome(reason); got != want {
			t.Fatalf("%s -> %s, want %s", reason, got, want)
		}
	}
}

func TestAskBodyIsReadableAndOmitsFormHTML(t *testing.T) {
	s, _ := testBox(t)
	base := sample("q", Ask, Delivered)
	base.FromRunID, base.AwaitingID = "r22", "a9"
	question := base
	question.AwaitingMode, question.AwaitingSource = "question", "ask_user_question"
	question.Message = jsonText(map[string]any{"questions": []any{map[string]any{"question": "Which repo?", "type": "select", "options": []any{map[string]any{"label": "skip"}, map[string]any{"label": "wait"}}}}})
	approval := base
	approval.AwaitingMode, approval.AwaitingSource = "approval", "tool_approval"
	approval.Message = jsonText(map[string]any{"approvals": []any{map[string]any{"toolName": "bash", "command": "rm -rf build/", "options": []any{map[string]any{"decision": "approve"}, map[string]any{"decision": "approve_rule_run"}}}}})
	form := base
	form.AwaitingMode, form.AwaitingSource = "form", "ask_user_form"
	form.Message = jsonText(map[string]any{"form": map[string]any{"title": "Region", "data": map[string]any{"html": "<select name=region><option>cn</option></select>"}}, "fields": []any{map[string]any{"name": "region", "type": "select", "options": []any{"cn"}}}})
	for _, tc := range []struct {
		mail Mail
		want []string
	}{
		{question, []string{"1. Which repo?  select: skip | wait"}},
		{approval, []string{"bash: rm -rf build/", "Options: approve | approve_rule_run | reject.", "your own access level"}},
		{form, []string{"Region", "- region (select): cn", "approve with data"}},
	} {
		body := s.Body(tc.mail)
		if strings.Contains(body, "<select") || !strings.Contains(body, `chat_submit runId="r22" awaitingId="a9"`) {
			t.Fatalf("%s body: %s", tc.mail.AwaitingMode, body)
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%s body lacks %q: %s", tc.mail.AwaitingMode, want, body)
			}
		}
	}
}
