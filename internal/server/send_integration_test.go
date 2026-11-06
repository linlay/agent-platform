package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
	platformws "agent-platform/internal/ws"
)

func bindUserMailbox(t *testing.T, f testFixture) *mailbox.Service {
	t.Helper()
	store, err := mailbox.Open(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	box := mailbox.New(context.Background(), store, f.cfg.Paths.ChatsDir, 4, nil)
	testQueryService(f.server).BindMailbox(box)
	f.server.deps.Mailbox = box
	t.Cleanup(func() {
		if err := box.Close(); err != nil {
			t.Error(err)
		}
	})
	return box
}
func sendHTTP(t *testing.T, f testFixture, input string) (int, mailbox.Receipt) {
	t.Helper()
	w := httptest.NewRecorder()
	f.server.handleSend(w, httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(input)))
	var result struct {
		Data mailbox.Receipt `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if w.Code != 200 {
		t.Log(w.Body.String())
	}
	return w.Code, result.Data
}
func TestUserSendStartsOrdinaryRunAndIsIdempotent(t *testing.T) {
	f := newTestFixture(t)
	box := bindUserMailbox(t, f)
	body := `{"requestId":"user-1","agentKey":"mock-agent","message":"hello from a person"}`
	code, receipt := sendHTTP(t, f, body)
	if code != 200 || receipt.Delivery != "started" || receipt.RunID == "" || receipt.ChatID == "" {
		t.Fatalf("receipt: %d %#v", code, receipt)
	}
	var m mailbox.Mail
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, _ = box.Store.Get(receipt.MailID)
		if m.Status == mailbox.Done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if m.Status != mailbox.Done || m.AnsweredBy != "" || m.Header != "" || m.OriginType != "user" || m.FromRunID != "" {
		t.Fatalf("user mail: %#v", m)
	}
	line, err := f.chats.LoadRunQuery(receipt.ChatID, receipt.RunID)
	if err != nil || line == nil || line.RequestID != "user-1" || line.MailInput == nil || line.MailInput.OriginType != "user" || line.Origin == nil || line.Origin.Type != queryinput.OriginUser {
		t.Fatalf("query: %#v %v", line, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, r := sendHTTP(t, f, body)
			if c != 200 || r.MailID != receipt.MailID {
				t.Errorf("replay: %d %#v", c, r)
			}
		}()
	}
	wg.Wait()
	n, _ := box.Store.Count(mailbox.Filter{AllSubjects: true})
	if n != 1 {
		t.Fatalf("duplicate/reply count=%d", n)
	}
	code, _ = sendHTTP(t, f, strings.Replace(body, "hello from a person", "changed", 1))
	if code != 409 {
		t.Fatalf("conflict=%d", code)
	}
}
func TestUserSendBusyDeliveryAndStrictProtocol(t *testing.T) {
	f := newTestFixture(t)
	box := bindUserMailbox(t, f)
	_, _, err := f.chats.EnsureChatWithSourceAndMode("busy-user", "mock-agent", "busy", "query:", "GENERAL")
	if err != nil {
		t.Fatal(err)
	}
	session := contracts.QuerySession{RunID: "busy-run", ChatID: "busy-user", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent"), AccessLevel: "ask_for_approval"}
	_, control, _ := f.runs.Register(context.Background(), session)
	defer f.runs.Finish(session.RunID)
	f.runs.BindRunControlScope(session.RunID, controlscope.Scope{Transport: "http"})
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "pending-question", Mode: "question", ItemCount: 1})
	control.SetSteerPreparer(func(r queryinput.SteerRequest) (queryinput.SteerRequest, error) { return r, nil })
	code, receipt := sendHTTP(t, f, `{"requestId":"insert","chatId":"busy-user","message":"another input","planningMode":true,"editingMode":true}`)
	if code != 200 || receipt.Delivery != "inserted" || receipt.RunID != session.RunID {
		t.Fatalf("insert: %d %#v", code, receipt)
	}
	if len(receipt.IgnoredOptions) != 2 {
		t.Fatalf("ignored: %#v", receipt)
	}
	if _, pending := control.LookupAwaiting("pending-question"); !pending {
		t.Fatal("user mail answered the active question")
	}
	steers := control.DrainSteers()
	if len(steers) != 1 || steers[0].RequestID != "insert" || steers[0].Origin != nil || steers[0].Mail.Header != "" {
		t.Fatalf("steers: %#v", steers)
	}
	control.TransitionState(contracts.RunLoopStateCompacting)
	code, queued := sendHTTP(t, f, `{"requestId":"queue","chatId":"busy-user","message":"after compact"}`)
	if code != 200 || queued.Delivery != "queued" || queued.RunID != "" {
		t.Fatalf("queue: %d %#v", code, queued)
	}
	m, _ := box.Store.Get(queued.MailID)
	if m.Status != mailbox.Queued || m.EditingMode == nil || m.Detail == "" {
		t.Fatalf("not frozen: %#v", m)
	}
	if m.Model != nil {
		t.Fatalf("omitted model was frozen as an explicit choice: %#v", m.Model)
	}
	for _, body := range []string{
		`{"requestId":"bad","agentKey":"mock-agent","message":"x","hidden":true}`,
		`{"requestId":"bad","agentKey":"mock-agent","message":"x","lane":"btw"}`,
		`{"requestId":"bad","agentKey":"mock-agent","message":"x","model":{"key":"wrong"}}`,
		`{"requestId":"bad","agentKey":"mock-agent","message":"x","model":{"modelKey":"mock-model"}}`,
		`{"requestId":"bad","agentKey":"mock-agent","message":" "}`,
		`{"requestId":" ","agentKey":"mock-agent","message":"x"}`,
	} {
		if c, _ := sendHTTP(t, f, body); c != 400 {
			t.Fatalf("strict body %s: %d", body, c)
		}
	}
}

func TestUserSendWebSocketSharesHTTPIdempotency(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: platformws.NewHub()})
	bindUserMailbox(t, f)
	conn := connectorUsageSocket(t, f.server)
	input := map[string]any{"requestId": "same-entry", "agentKey": "mock-agent", "message": "one message"}
	sendConnectorUsageRequest(t, conn, "/api/send", "send-frame", input)
	receipt := waitForWebSocketResponseData[mailbox.Receipt](t, conn, "send-frame")
	if receipt.Delivery != "started" || receipt.RunID == "" {
		t.Fatalf("WS receipt: %#v", receipt)
	}
	raw, _ := json.Marshal(input)
	code, replay := sendHTTP(t, f, string(raw))
	if code != 200 || replay.MailID != receipt.MailID {
		t.Fatalf("HTTP replay: %d %#v", code, replay)
	}
}

func TestUserSendWebSocketDetailLevels(t *testing.T) {
	for _, detail := range []string{"simple", "detailed"} {
		t.Run(detail, func(t *testing.T) {
			f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: platformws.NewHub()})
			box := bindUserMailbox(t, f)
			conn := connectorUsageSocket(t, f.server)
			frame := platformws.RequestFrame{
				Frame: platformws.FrameRequest, Type: "/api/send", ID: "send-detail",
				Detail: detail,
				Payload: marshalPayload(map[string]any{
					"requestId": "detail-request", "agentKey": "mock-agent", "message": "hello",
				}),
			}
			if err := conn.WriteJSON(frame); err != nil {
				t.Fatal(err)
			}
			receipt := waitForWebSocketResponseData[mailbox.Receipt](t, conn, frame.ID)
			if receipt.Delivery != "started" || receipt.RunID == "" {
				t.Fatalf("WS receipt: %#v", receipt)
			}
			mail, err := box.Store.Get(receipt.MailID)
			if err != nil || mail.Detail != detail {
				t.Fatalf("mail detail=%q, want %q: %v", mail.Detail, detail, err)
			}
		})
	}
}

func TestUserSendProxyStartsWhenIdleAndQueuesWhileBusy(t *testing.T) {
	captured := make(chan map[string]any, 2)
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		captured <- input
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"run.complete\",\"runId\":\"upstream\",\"timestamp\":%d}\n\n", time.Now().UnixMilli())
	}))
	defer upstream.Close()
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
			"key: mock-agent", "name: Proxy Agent", "mode: PROXY", "modelConfig:", "  modelKey: mock-model",
			"proxyConfig:", "  localACP: true", "  baseUrl: " + upstream.URL, "  transport: sse",
		})
	}})
	box := bindUserMailbox(t, f)
	code, started := sendHTTP(t, f, `{"requestId":"proxy-start","agentKey":"mock-agent","message":"ordinary input"}`)
	if code != 200 || started.Delivery != "started" {
		t.Fatalf("start: %d %#v", code, started)
	}
	select {
	case input := <-captured:
		if input["message"] != "ordinary input" {
			t.Fatal(input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not receive user input")
	}
	_, _, err := f.chats.EnsureChatWithSourceAndMode("proxy-busy", "mock-agent", "busy", "query:", "PROXY")
	if err != nil {
		t.Fatal(err)
	}
	defer f.runs.Finish("proxy-active")
	f.runs.Register(context.Background(), contracts.QuerySession{RunID: "proxy-active", ChatID: "proxy-busy", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent")})
	code, queued := sendHTTP(t, f, `{"requestId":"proxy-queue","chatId":"proxy-busy","message":"after idle"}`)
	if code != 200 || queued.Delivery != "queued" || queued.RunID != "" {
		t.Fatalf("queue: %d %#v", code, queued)
	}
	select {
	case input := <-captured:
		t.Fatalf("busy proxy received an insert: %#v", input)
	default:
	}
	f.runs.Finish("proxy-active")
	box.Kick("proxy-busy")
	select {
	case input := <-captured:
		if input["message"] != "after idle" {
			t.Fatal(input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued proxy input was not delivered")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := box.Store.Get(queued.MailID)
		if m.Status == mailbox.Done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	m, _ := box.Store.Get(queued.MailID)
	t.Fatalf("proxy user mail did not settle: %#v", m)
}

func waitUserMail(t *testing.T, box *mailbox.Service, id string) mailbox.Mail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	m, _ := box.Store.Get(id)
	for time.Now().Before(deadline) && !m.Terminal() {
		time.Sleep(time.Millisecond)
		m, _ = box.Store.Get(id)
	}
	return m
}

func TestUserSendOmittedModelFollowsAgentPolicy(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(content, []byte("\ninteractionConfig:\n  model: false\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	box := bindUserMailbox(t, f)
	code, receipt := sendHTTP(t, f, `{"requestId":"no-model","agentKey":"mock-agent","message":"hello"}`)
	if code != 200 || receipt.Delivery != "started" || receipt.Status == mailbox.Failed {
		t.Fatalf("receipt: %d %#v", code, receipt)
	}
	if m := waitUserMail(t, box, receipt.MailID); m.Status != mailbox.Done || m.Model != nil {
		t.Fatalf("user mail: %s %#v %#v", m.Status, m.Error, m.Model)
	}
	if c, _ := sendHTTP(t, f, `{"requestId":"with-model","agentKey":"mock-agent","message":"hello","model":{"key":"mock-model"}}`); c != 400 {
		t.Fatalf("explicit model on a model-locked Agent: %d", c)
	}
}

func TestUserSendAcceptsReferencesWithoutText(t *testing.T) {
	f := newTestFixture(t)
	box := bindUserMailbox(t, f)
	_, first := sendHTTP(t, f, `{"requestId":"first","agentKey":"mock-agent","message":"start"}`)
	if m := waitUserMail(t, box, first.MailID); m.Status != mailbox.Done {
		t.Fatalf("first: %#v", m)
	}
	dir := filepath.Join(f.cfg.Paths.ChatsDir, first.ChatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"requestId":"refs-only","chatId":%q,"references":[{"type":"file","name":"notes.txt","url":"@chat/notes.txt"}]}`, first.ChatID)
	code, receipt := sendHTTP(t, f, body)
	if code != 200 || receipt.Delivery != "started" {
		t.Fatalf("references only: %d %#v", code, receipt)
	}
	if m := waitUserMail(t, box, receipt.MailID); m.Status != mailbox.Done {
		t.Fatalf("references only: %s %#v", m.Status, m.Error)
	}
	empty := fmt.Sprintf(`{"requestId":"empty","chatId":%q}`, first.ChatID)
	if c, _ := sendHTTP(t, f, empty); c != 400 {
		t.Fatalf("empty send: %d", c)
	}
}
