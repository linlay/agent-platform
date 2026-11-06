package acp_test

import (
	"agent-platform/internal/agentengine"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/connector"
	dto "agent-platform/internal/contracts/agentengine"
	driver "agent-platform/internal/runtime/acp"
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/coder/acp-go-sdk"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const sessionAgent = `let count=0,model="Model-A";const send=(id,result)=>console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
const options=()=>[{id:'ModelID',name:'Model',category:'model',type:'select',currentValue:model,options:[{value:'Model-A',name:'A'},{value:'Model-B',name:'B'}]}];
require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);switch(m.method){
case 'initialize':send(m.id,{protocolVersion:1,agentCapabilities:{loadSession:true,sessionCapabilities:{resume:{}}}});break;
case 'session/set_config_option':model=m.params.value;send(m.id,{configOptions:options()});break;
case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;
case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;
default:console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32002,message:'session unavailable'}}));
}});`

const multiplexAgent = `const fs=require('node:fs');let count=0;const sessions=new Map();
const send=(id,result)=>console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
const options=model=>[{id:'ModelID',name:'Model',category:'model',type:'select',currentValue:model,options:[{value:'Model-A',name:'A'},{value:'Model-B',name:'B'}]}];
require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line),p=m.params;switch(m.method){
case 'initialize':fs.appendFileSync('initializations','1\n');send(m.id,{protocolVersion:1,agentCapabilities:{loadSession:true,sessionCapabilities:{resume:{},close:{}}}});break;
case 'session/new':{const id='session-'+(++count);sessions.set(id,'Model-A');send(m.id,{sessionId:id,configOptions:options('Model-A')});break;}
case 'session/set_config_option':sessions.set(p.sessionId,p.value);send(m.id,{configOptions:options(p.value)});break;
case 'session/prompt':send(m.id,{stopReason:'end_turn',model:sessions.get(p.sessionId),sessionId:p.sessionId});break;
case 'session/close':sessions.delete(p.sessionId);send(m.id,{});break;
default:send(m.id,{});
}});`

func TestOversizedPromptIsRejectedWithoutClosingSession(t *testing.T) {
	_, store, host, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	prepared, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sessions.Prompt(ctx, prepared.ChatID, prepared.PreparationID, prepared.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock(strings.Repeat("x", 64<<20))}, nil)
	var call *driver.CallError
	if !errors.Is(err, agentengine.Error("resource_reference_too_large")) || !errors.As(err, &call) || call.ExecutionState != "not_started" {
		t.Fatalf("oversize must be an explicit pre-send rejection: %v", err)
	}
	binding, err := store.LoadEngineSession(prepared.ChatID)
	if err != nil || binding == nil || binding.ContextState != "prepared" {
		t.Fatalf("rejected prompt changed conversation: %+v %v", binding, err)
	}
	if _, err := sessions.Prompt(ctx, prepared.ChatID, prepared.PreparationID, prepared.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("retry with smaller content")}, nil); err != nil {
		t.Fatalf("rejected prompt broke session: %v", err)
	}
	if host.Stats().Processes != 1 {
		t.Fatalf("rejected prompt closed process: %+v", host.Stats())
	}
}

func TestChatsShareEngineProcessAndKeepSessionConfigurationIsolated(t *testing.T) {
	_, store, host, sessions, root := sessionFixture(t, multiplexAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.EnsureChat("chat-two", "project", ""); err != nil {
		t.Fatal(err)
	}
	second, err := sessions.Prepare(ctx, "chat-two")
	if err != nil {
		t.Fatal(err)
	}
	if host.Stats().Processes != 1 {
		t.Fatalf("two Chats should share one engine process: %+v", host.Stats())
	}
	if first.PreparationID == second.PreparationID {
		t.Fatal("preparation handles must be session scoped")
	}
	configured, err := sessions.Configure(ctx, dto.ConfigUpdate{ChatID: first.ChatID, PreparationID: first.PreparationID, ConfigRevision: first.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []dto.PreparedSession{configured, second} {
		raw, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hello")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "Model-A"
		if p.ChatID == first.ChatID {
			want = "Model-B"
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("configuration crossed Chats: %s", raw)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "initializations")); err != nil || string(data) != "1\n" {
		t.Fatalf("repeated initialization: %q %v", data, err)
	}
	if err := sessions.CloseChat(first.ChatID); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prompt(ctx, second.ChatID, second.PreparationID, second.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("still running")}, nil); err != nil {
		t.Fatalf("closing another Chat stopped worker: %v", err)
	}
}

func TestSharedWorkerRoutesPermissionsAndCancelsOnlyRequestedSession(t *testing.T) {
	script := strings.Replace(multiplexAgent, "const sessions=new Map();", "const sessions=new Map(),prompts=new Map();", 1)
	script = strings.Replace(script, "case 'session/prompt':send(m.id,{stopReason:'end_turn',model:sessions.get(p.sessionId),sessionId:p.sessionId});break;", `case 'session/prompt':{
prompts.set(p.sessionId,m.id);
console.log(JSON.stringify({jsonrpc:'2.0',id:'ask-'+p.sessionId,method:'session/request_permission',params:{sessionId:p.sessionId}}));
const timer=setInterval(()=>{if(!prompts.has(p.sessionId)){clearInterval(timer);return;}if(fs.existsSync('finish-'+p.sessionId)){clearInterval(timer);prompts.delete(p.sessionId);send(m.id,{stopReason:'end_turn'});}},10);break;}
case 'session/cancel':{const id=prompts.get(p.sessionId);prompts.delete(p.sessionId);if(id)send(id,{stopReason:'cancelled'});break;}`, 1)
	_, store, host, sessions, root := sessionFixtureWithOptions(t, script, driver.Options{MaxSessions: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var prepared []dto.PreparedSession
	for _, id := range []string{"chat-one", "chat-two"} {
		if _, _, err := store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		prepared = append(prepared, p)
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	permissions := make(chan string, 2)
	finished := []chan error{make(chan error, 1), make(chan error, 1)}
	for i, p := range prepared {
		turn := ctx
		if i == 0 {
			turn = firstCtx
		}
		go func(i int, p dto.PreparedSession) {
			_, err := sessions.Prompt(turn, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("wait")}, func(_ context.Context, in driver.Incoming) (any, *sdk.RequestError) {
				var params struct {
					SessionID string `json:"sessionId"`
				}
				json.Unmarshal(in.Params, &params)
				binding, _ := store.LoadEngineSession(p.ChatID)
				if binding == nil || params.SessionID != binding.ACPSessionID {
					permissions <- "wrong-chat"
				} else {
					permissions <- p.ChatID
				}
				return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
			})
			finished[i] <- err
		}(i, p)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-permissions:
			seen[id] = true
		case err := <-finished[0]:
			t.Fatalf("first prompt ended before approvals: %v", err)
		case err := <-finished[1]:
			t.Fatalf("second prompt ended before approvals: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !seen["chat-one"] || !seen["chat-two"] {
		t.Fatal(seen)
	}
	if _, _, err := store.EnsureChat("chat-pressure", "project", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prepare(ctx, "chat-pressure"); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("active prompt was evicted: %v", err)
	}
	stopFirst()
	select {
	case <-finished[0]:
	case <-ctx.Done():
		t.Fatal("first cancellation stuck")
	}
	select {
	case err := <-finished[1]:
		t.Fatalf("sibling prompt interrupted: %v", err)
	default:
	}
	binding, _ := store.LoadEngineSession("chat-two")
	write(t, filepath.Join(root, "finish-"+binding.ACPSessionID), "", 0600)
	select {
	case err := <-finished[1]:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if host.Stats().Processes != 1 {
		t.Fatal("healthy cancellation must retain shared process")
	}
}

func TestResidentSessionBudgetRestoresEvictedChatMemory(t *testing.T) {
	script := strings.Replace(multiplexAgent, "const sessions=new Map();", "const sessions=new Map(),memories=new Map();", 1)
	script = strings.Replace(script, "case 'session/prompt':send", "case 'session/prompt':if(p.prompt[0].text==='remember'){memories.set(p.sessionId,'unique-memory');}send", 1)
	script = strings.Replace(script, "model:sessions.get(p.sessionId),sessionId:p.sessionId", "model:sessions.get(p.sessionId),memory:memories.get(p.sessionId),sessionId:p.sessionId", 1)
	script = strings.Replace(script, "case 'session/close':", `case 'session/resume': {const saved=JSON.parse(fs.readFileSync('saved-'+p.sessionId));sessions.set(p.sessionId,saved.model);memories.set(p.sessionId,saved.memory);send(m.id,{sessionId:p.sessionId,configOptions:options(saved.model)});break;}
 case 'session/close':fs.writeFileSync('saved-'+p.sessionId,JSON.stringify({model:sessions.get(p.sessionId),memory:memories.get(p.sessionId)}));memories.delete(p.sessionId);`, 1)
	_, store, host, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	first, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: first.ChatID, PreparationID: first.PreparationID, ConfigRevision: first.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, first.ChatID, first.PreparationID, first.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("remember")}, nil); err != nil {
		t.Fatal(err)
	}
	binding, _ := store.LoadEngineSession(first.ChatID)
	for i := 0; i < 8; i++ {
		id := "chat-budget-" + strconv.Itoa(i)
		if _, _, err = store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		if _, err = sessions.Prepare(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = sessions.Inspect(first.ChatID); err == nil {
		t.Fatal("oldest idle session still resident after budget exhausted")
	}
	restored, err := sessions.Prepare(ctx, first.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.LoadEngineSession(first.ChatID)
	if after.ACPSessionID != binding.ACPSessionID || restored.PreparationID == first.PreparationID {
		t.Fatal("restore lost identity or reused stale preparation")
	}
	raw, err := sessions.Prompt(ctx, restored.ChatID, restored.PreparationID, restored.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("recall")}, nil)
	if err != nil || !strings.Contains(string(raw), "Model-B") || !strings.Contains(string(raw), "unique-memory") {
		t.Fatalf("restored memory/config: %s %v", raw, err)
	}
	if host.Stats().Processes != 1 {
		t.Fatal(host.Stats())
	}
}

func TestPreparePersistsAndReusesRealChatSession(t *testing.T) {
	_, store, host, sessions, _ := sessionFixture(t, sessionAgent)
	var err error
	if _, err = sessions.Inspect("chat-one"); err == nil {
		t.Fatal("unprepared chat has session")
	}
	if host.Stats().Processes != 0 {
		t.Fatal("inspection spawned process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if first.PreparationID == "" || first.PreparationID != second.PreparationID || len(first.ConfigOptions) != 1 {
		t.Fatalf("preparation not reused: %+v %+v", first, second)
	}
	var option map[string]any
	if err = json.Unmarshal(first.ConfigOptions[0], &option); err != nil {
		t.Fatal(err)
	}
	if option["id"] != "ModelID" || option["currentValue"] != "Model-A" || option["choices"] == nil {
		t.Fatalf("external options changed: %+v", option)
	}
	binding, err := store.LoadEngineSession("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || binding.ACPSessionID != "session-1" || binding.ContextState != "prepared" || binding.ProfileID == "" {
		t.Fatalf("binding not persisted: %+v", binding)
	}
	if host.Stats().Processes != 1 {
		t.Fatal("prepare started multiple processes")
	}
}

// A real Adapter holds session/new while multiple HTTP callers prepare one Chat.
func TestConcurrentPreparationSharesOneSession(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", `case 'session/new': {
const fs=require('node:fs');fs.writeFileSync('preparing','');
const timer=setInterval(()=>{if(fs.existsSync('release-prepare')){clearInterval(timer);send(m.id,{sessionId:'session-'+(++count),configOptions:options()});}},10);break;}`, 1)
	_, _, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		prepared dto.PreparedSession
		err      error
	}
	prepare := func(ch chan<- result) { p, err := sessions.Prepare(ctx, "chat-one"); ch <- result{p, err} }
	first, second := make(chan result, 1), make(chan result, 1)
	go prepare(first)
	for {
		if _, err := os.Stat(filepath.Join(root, "preparing")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Adapter did not reach session/new")
		case <-time.After(10 * time.Millisecond):
		}
	}
	go prepare(second)
	select {
	case r := <-second:
		t.Fatalf("overlapping prepare returned before Adapter was ready: %v", r.err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "release-prepare"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("prepare failed: %v / %v", a.err, b.err)
	}
	if a.prepared.PreparationID != b.prepared.PreparationID || host.Stats().Processes != 1 {
		t.Fatal("overlapping prepares started separate sessions")
	}
}

func TestLeavingPreparationDoesNotRestartAdapterOnReturn(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", `case 'session/new': {
const fs=require('node:fs');fs.appendFileSync('preparation-starts','start\n');
const timer=setInterval(()=>{if(fs.existsSync('release-prepare')){clearInterval(timer);send(m.id,{sessionId:'session-'+(++count),configOptions:options()});}},10);break;}`, 1)
	_, _, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leaving, leave := context.WithCancel(ctx)
	defer leave()
	first := make(chan error, 1)
	go func() { _, err := sessions.Prepare(leaving, "chat-one"); first <- err }()
	for {
		if _, err := os.Stat(filepath.Join(root, "preparation-starts")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Adapter did not reach session/new")
		case <-time.After(10 * time.Millisecond):
		}
	}
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller did not cancel: %v", err)
	}
	// Another canceled waiter must not cancel the host-owned preparation either.
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := sessions.Prepare(canceled, "chat-one"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "release-prepare"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil || p.PreparationID == "" {
		t.Fatalf("returning Chat failed: %v", err)
	}
	starts, err := os.ReadFile(filepath.Join(root, "preparation-starts"))
	if err != nil || string(starts) != "start\n" || host.Stats().Processes != 1 {
		t.Fatalf("navigation restarted preparation: %q %v", starts, err)
	}
}

func sessionFixture(t *testing.T, script string, configure ...func(*dto.EngineResource)) (*agentengine.Service, *chat.FileStore, *driver.Supervisor, *driver.Sessions, string) {
	t.Helper()
	return sessionFixtureWithOptions(t, script, driver.Options{}, configure...)
}
func sessionFixtureWithOptions(t *testing.T, script string, options driver.Options, configure ...func(*dto.EngineResource)) (*agentengine.Service, *chat.FileStore, *driver.Supervisor, *driver.Sessions, string) {
	t.Helper()
	registry, root := fixture(t, script, configure...)
	store, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, _, err = store.EnsureChat("chat-one", "project", ""); err != nil {
		t.Fatal(err)
	}
	host, err := driver.New(registry, filepath.Join(root, "runtime"), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	sessions := driver.NewSessions(host, store, func(key string) (catalog.AgentDefinition, bool) {
		return catalog.AgentDefinition{Key: key, Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent", "workspaceRoot": root}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}, key == "project"
	})
	t.Cleanup(func() { sessions.Close() })
	return registry, store, host, sessions, root
}

func TestConfigurationIsConfirmedBeforeSameSessionPrompt(t *testing.T) {
	_, store, _, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prepared, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	update := dto.ConfigUpdate{ChatID: "chat-one", PreparationID: prepared.PreparationID, ConfigRevision: prepared.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}}
	configured, err := sessions.Configure(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	if configured.ConfigRevision <= prepared.ConfigRevision {
		t.Fatal("configuration revision not advanced")
	}
	if _, err = sessions.Configure(ctx, update); err == nil {
		t.Fatal("stale update accepted")
	}
	result, err := sessions.Prompt(ctx, "chat-one", configured.PreparationID, configured.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	json.Unmarshal(result, &value)
	if value["model"] != "Model-B" || value["sessionId"] != "session-1" {
		t.Fatalf("first prompt lost configuration or session: %s", result)
	}
	binding, err := store.LoadEngineSession("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if binding.ContextState != "persisted" || len(binding.ConfigSelection) != 1 || string(binding.ConfigSelection[0].Value) != `"Model-B"` {
		t.Fatalf("confirmed state not persisted: %+v", binding)
	}
}

func TestLostPreparedSessionRebuildsButSubmittedSessionNeverDoes(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "submitted"}[submitted], func(t *testing.T) {
			_, store, host, sessions, root := sessionFixture(t, sessionAgent)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			before, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := store.LoadEngineSession("chat-one")
			if err != nil {
				t.Fatal(err)
			}
			if submitted {
				binding.ContextState = "submitted"
				if err = store.SaveEngineSession(*binding); err != nil {
					t.Fatal(err)
				}
			}
			if err = sessions.CloseChat("chat-one"); err != nil {
				t.Fatal(err)
			}
			after, err := sessions.Prepare(ctx, "chat-one")
			if submitted {
				if err == nil || err.Error() != "session_restore_failed" {
					t.Fatalf("submitted session silently replaced: %+v %v", after, err)
				}
				if host.Stats().Processes != 0 {
					t.Fatal("failed restore retained process")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if after.PreparationID == before.PreparationID {
					t.Fatal("stale preparation survived rebuild")
				}
			}
			_ = root
		})
	}
}

func TestRestartRestoresExternalContextAndConfirmedConfig(t *testing.T) {
	script := strings.Replace(sessionAgent, "let count=0,model=\"Model-A\";", `const fs=require('node:fs'),file=require('node:path').join(process.env.ZENMIND_AGENT_PROFILE,'test-session.json');let turns=fs.existsSync(file)?JSON.parse(fs.readFileSync(file)).turns:0;let count=0,model="Model-A";`, 1)
	script = strings.Replace(script, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':fs.writeFileSync(file,JSON.stringify({turns:++turns}));send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model,turns});break;
 case 'session/resume':send(m.id,{configOptions:options()});break;`, 1)
	// A model's effort choices only become available after that model is restored.
	script = strings.Replace(script, "const options=()=>[", `const options=()=>[{id:'Effort',name:'Effort',type:'select',currentValue:model==='Model-B'?'low':'medium',options:[{value:model==='Model-B'?'low':'medium',name:'Effort'}]},`, 1)
	// Keep the adapter's dependency order: model first, then effort.
	script = strings.ReplaceAll(script, "configOptions:options()", "configOptions:options().reverse()")
	script = strings.Replace(script, "model=m.params.value;", "if(m.params.configId==='ModelID')model=m.params.value;", 1)

	registry, store, _, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	p, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: p.ChatID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil); err != nil {
		t.Fatal(err)
	}
	sessions.Close()
	restartedStore, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	defer restartedStore.Close()
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := driver.NewSessions(host, restartedStore, func(key string) (catalog.AgentDefinition, bool) {
		return catalog.AgentDefinition{Key: key, Engine: "acp", Runtime: map[string]any{"engineId": "fixture/agent", "workspaceRoot": root}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}, true
	})
	defer restarted.Close()
	restored, err := restarted.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if restored.PreparationID == p.PreparationID {
		t.Fatal("generation reused after restart")
	}
	raw, err := restarted.Prompt(ctx, restored.ChatID, restored.PreparationID, restored.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("two")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model string
		Turns int
	}
	json.Unmarshal(raw, &result)
	if result.Model != "Model-B" || result.Turns != 2 {
		t.Fatalf("restore lost configuration/context: %s", raw)
	}
	binding, err := store.LoadEngineSession("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if binding.LastUsed == nil || binding.LastUsed.ArtifactDigest == "" {
		t.Fatal("diagnostic version missing")
	}
}

func TestExternalConfigurationPreservesGroupsAndRejectsUnsupportedValues(t *testing.T) {
	script := strings.Replace(sessionAgent, "options:[{value:'Model-A',name:'A'},{value:'Model-B',name:'B'}]", "options:[{group:'vendor',name:'Vendor',options:[{value:'Model-A',name:'A'},{value:'Model-B',name:'B'}]}]", 1)
	// Unknown model_config semantics must stay closed regardless of product ID.
	script = strings.Replace(script, "const options=()=>[", "const options=()=>[{id:'vendor-tier',name:'Service tier',category:'model_config',type:'select',currentValue:'off',options:[{value:'off',name:'Off'},{value:'on',name:'On'}]},", 1)
	_, _, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ConfigOptions) != 1 {
		t.Fatalf("unverified service tier exposed: %s", p.ConfigOptions)
	}
	var option struct {
		Choices []dto.ConfigChoice `json:"choices"`
	}
	json.Unmarshal(p.ConfigOptions[0], &option)
	if len(option.Choices) != 2 || option.Choices[1].Value != "Model-B" || option.Choices[0].Group == nil || *option.Choices[0].Group != "vendor" {
		t.Fatalf("group/value changed: %+v", option)
	}
	for _, selection := range [][]dto.ConfigSelection{
		{{ConfigID: "modelid", Value: json.RawMessage(`"Model-B"`)}},
		{{ConfigID: "ModelID", Value: json.RawMessage(`"model-b"`)}},
		{{ConfigID: "serviceTier", Value: json.RawMessage(`"fast"`)}},
		{{ConfigID: "fast-mode", Value: json.RawMessage(`"on"`)}},
		{{ConfigID: "ModelID", Value: json.RawMessage(`true`)}},
		{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}, {ConfigID: "ModelID", Value: json.RawMessage(`"Model-A"`)}},
	} {
		if _, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: p.ChatID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: selection}); err == nil {
			t.Fatalf("invalid selection accepted: %+v", selection)
		}
	}
}

func TestPromptPersistsSubmittedBeforeReverseRequest(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':global.pending=m.id;console.log(JSON.stringify({jsonrpc:'2.0',id:'question',method:'elicitation/create',params:{sessionId:m.params.sessionId,toolCallId:'tool'}}));break;
 case undefined:if(m.error)process.exit(1);send(global.pending,{stopReason:'end_turn'});break;`, 1)
	_, store, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	_, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hello")}, func(ctx context.Context, in driver.Incoming) (any, *sdk.RequestError) {
		called.Store(true)
		binding, err := store.LoadEngineSession("chat-one")
		if err != nil || binding.ContextState != "submitted" {
			t.Errorf("prompt dispatched before durable state: %+v %v", binding, err)
		}
		if in.Generation != p.PreparationID || string(in.ID) != `"question"` {
			t.Error("lost interaction identity")
		}
		return map[string]any{"action": "cancel"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("reverse request never reached current session handler")
	}
}

func TestLoadConsumesStateUpdatesWithoutReplayingHistory(t *testing.T) {
	script := strings.Replace(sessionAgent, "sessionCapabilities:{resume:{}}", "sessionCapabilities:{resume:false}", 1)
	script = strings.Replace(script, "case 'session/new':", `case 'session/load':
 console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'OLD HISTORY'}}}}));
 model='Model-B';console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'config_option_update',configOptions:options()}}}));
 console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'available_commands_update',availableCommands:[{name:'compact',description:'Compact'}]}}}));
 console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'current_mode_update',currentModeId:'default'}}}));
 send(m.id,{});break;
 case 'session/new':`, 1)
	script = strings.Replace(script, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:[]});break;", 1)
	_, _, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("first")}, nil); err != nil {
		t.Fatal(err)
	}
	sessions.CloseChat("chat-one")
	restored, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.ConfigOptions) != 1 || !strings.Contains(string(restored.ConfigOptions[0]), `"currentValue":"Model-B"`) {
		t.Fatalf("load lost config notifications: %+v", restored)
	}
	details, err := sessions.Details("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(details.AvailableCommands) != 1 || details.CurrentModeID != "default" {
		t.Fatalf("load lost current state: %+v", details)
	}
	var callbacks atomic.Int32
	if _, err = sessions.Prompt(ctx, p.ChatID, restored.PreparationID, restored.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("second")}, func(context.Context, driver.Incoming) (any, *sdk.RequestError) { callbacks.Add(1); return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Fatal("restore history was replayed into current prompt")
	}
}

func TestRevocationBlocksPreparedPromptWithoutLosingBinding(t *testing.T) {
	registry, store, host, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("must not send")}, nil); err == nil || (err.Error() != "engine_unavailable" && err.Error() != "engine_busy" && err.Error() != "session_configuration_stale") {
		t.Fatalf("revoked prompt admitted: %v", err)
	}
	binding, err := store.LoadEngineSession("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if binding.ContextState != "prepared" {
		t.Fatal("rejected prompt changed durable state")
	}
	for host.Stats().Processes != 0 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("revoked idle process retained")
	}
}

func TestAuthenticationFailureDoesNotCreatePreparedBinding(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", `case 'session/new':console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'secret raw authentication details'}}));break;`, 1)
	_, store, host, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err == nil || err.Error() != "auth_required" {
		t.Fatalf("authentication misclassified: %v", err)
	}
	binding, err := store.LoadEngineSession("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil || host.Stats().Processes != 0 {
		t.Fatal("authentication failure left prepared state")
	}
}

func TestConfirmedDefaultModelSurvivesChangedRestoreDefault(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/new':", `case 'session/resume':model='Model-B';send(m.id,{configOptions:options()});break;case 'session/new':`, 1)
	_, _, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("first")}, nil); err != nil {
		t.Fatal(err)
	}
	sessions.CloseChat("chat-one")
	restored, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored.ConfigOptions[0]), `"currentValue":"Model-A"`) {
		t.Fatalf("confirmed default drifted: %s", restored.ConfigOptions[0])
	}
}

func TestDynamicConfigInvalidatesOldRevisionAndPersistsCurrentState(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':model='Model-B';console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'config_option_update',configOptions:options()}}}));setTimeout(()=>send(m.id,{stopReason:'end_turn'}),30);break;`, 1)
	_, store, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil)
		result <- err
	}()
	for i := 0; i < 50; i++ {
		if _, err = sessions.Inspect(p.ChatID); err != nil {
			t.Fatal(err)
		}
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	updated, err := sessions.Inspect(p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConfigRevision <= p.ConfigRevision {
		t.Fatal("dynamic options did not invalidate revision")
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("stale")}, nil); err == nil {
		t.Fatal("stale config prompt accepted")
	}
	binding, err := store.LoadEngineSession(p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if string(binding.ConfigSelection[0].Value) != `"Model-B"` {
		t.Fatalf("dynamic config lost: %+v", binding.ConfigSelection)
	}
}

func TestUnconfirmedPromptResultKeepsSubmittedState(t *testing.T) {
	for _, reply := range []string{"send(m.id,{})", "process.exit(23)"} {
		t.Run(reply, func(t *testing.T) {
			script := strings.Replace(sessionAgent, "send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model})", reply, 1)
			_, store, _, sessions, _ := sessionFixture(t, script)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil); err == nil {
				t.Fatal("unconfirmed prompt succeeded")
			}
			binding, err := store.LoadEngineSession(p.ChatID)
			if err != nil {
				t.Fatal(err)
			}
			if binding.ContextState != "submitted" {
				t.Fatalf("uncertain prompt became %s", binding.ContextState)
			}
		})
	}
}

func TestUpgradeRestoresUsingCurrentSnapshotWithoutPinningOldVersion(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/new':", `case 'session/resume':send(m.id,{configOptions:options()});break;case 'session/new':`, 1)
	registry, store, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := store.LoadEngineSession(p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	upgrade := sessionDelivery(t, root, script, "fixture", "2.0.0", "install2")
	_, err = registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{upgrade}}))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for host.Stats().Processes != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("idle session retained retired process")
	}
	old := filepath.Join(root, "runtime", "ru-agent-engines", "fixture", "agent", strings.TrimPrefix(before.LastUsed.ArtifactDigest, "sha256:"))
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("chat permanently pinned old program: %v", err)
	}
	restored, err := sessions.Prepare(ctx, p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadEngineSession(p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastUsed.PluginVersion != "2.0.0" || after.ACPSessionID != before.ACPSessionID || after.ProfileID != before.ProfileID || restored.PreparationID == p.PreparationID {
		t.Fatalf("upgrade binding drifted: %+v", after)
	}
}

func TestPrepareRetainsResponseMode(t *testing.T) {
	script := strings.Replace(sessionAgent, "sessionId:'session-'+(++count),configOptions:options()", "sessionId:'session-'+(++count),configOptions:options(),modes:{currentModeId:'plan',availableModes:[{id:'plan',name:'Plan'}]}", 1)
	_, _, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal(err)
	}
	details, err := sessions.Details("chat-one")
	if err != nil || details.CurrentModeID != "plan" {
		t.Fatalf("response mode lost: %+v %v", details, err)
	}
}

func TestRestoreRejectsInvalidResults(t *testing.T) {
	for _, reply := range []string{`{sessionId:"replacement",configOptions:options()}`, `"malformed"`, `{configOptions:[{id:"broken"}]}`} {
		t.Run(reply, func(t *testing.T) {
			script := strings.Replace(sessionAgent, "case 'session/new':", "case 'session/resume':send(m.id,"+reply+");break;case 'session/new':", 1)
			_, store, _, sessions, _ := sessionFixture(t, script)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("first")}, nil); err != nil {
				t.Fatal(err)
			}
			if err = sessions.CloseChat(p.ChatID); err != nil {
				t.Fatal(err)
			}
			if _, err = sessions.Prepare(ctx, p.ChatID); err == nil || err.Error() != "session_restore_failed" {
				t.Fatalf("silently replaced saved session: %v", err)
			}
			binding, err := store.LoadEngineSession(p.ChatID)
			if err != nil || binding.ACPSessionID != "session-1" || binding.ContextState != "persisted" {
				t.Fatalf("saved context changed: %+v %v", binding, err)
			}
		})
	}
}

func TestUnsentPromptPreservesPreviousContextState(t *testing.T) {
	_, store, _, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"prepared", "persisted"} {
		cancelled, stop := context.WithCancel(ctx)
		stop()
		_, err = sessions.Prompt(cancelled, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("must not send")}, nil)
		var call *driver.CallError
		if !errors.As(err, &call) || call.ExecutionState != "not_started" {
			t.Fatalf("unsent prompt: %v", err)
		}
		binding, err := store.LoadEngineSession(p.ChatID)
		if err != nil || binding.ContextState != state {
			t.Fatalf("unsent prompt changed %s: %+v %v", state, binding, err)
		}
		if state == "prepared" {
			if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("first")}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLifecycleDrainsActivePromptAndPreservesProfile(t *testing.T) {
	for _, action := range []string{"revoke", "disable", "upgrade"} {
		t.Run(action, func(t *testing.T) {
			script := strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':require('node:fs').writeFileSync(process.env.ZENMIND_AGENT_PROFILE+'/retained','profile');console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'started'}}}}));const timer=setInterval(()=>{if(require('node:fs').existsSync('release-turn')){clearInterval(timer);send(m.id,{stopReason:'end_turn'})}},10);break;`, 1)
			script = strings.Replace(script, "case 'session/new':", "case 'session/resume':send(m.id,{configOptions:options()});break;case 'session/new':", 1)
			registry, store, host, sessions, root := sessionFixture(t, script)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, 1)
			finished := make(chan error, 1)
			go func() {
				_, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, func(context.Context, driver.Incoming) (any, *sdk.RequestError) {
					started <- struct{}{}
					return nil, nil
				})
				finished <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if registry.Directory().Engines[0].RuntimeState == "stopped" {
				t.Fatal("live engine reported stopped")
			}
			entries := []dto.EngineInstallation{}
			if action == "upgrade" {
				entries = append(entries, sessionDelivery(t, root, script, "fixture", "2.0.0", "install2"))
			}
			if action == "disable" {
				data, err := os.ReadFile(filepath.Join(root, "desktop/data/agent-engines/install/record.json"))
				if err != nil {
					t.Fatal(err)
				}
				var record dto.TrustedDeliveryRecord
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, dto.EngineInstallation{EngineID: "fixture/agent", PluginID: "fixture", PluginVersion: "1.0.0", InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: false})
			}
			oldBinding, _ := store.LoadEngineSession(p.ChatID)
			oldSnapshot := filepath.Join(root, "runtime/ru-agent-engines/fixture/agent", strings.TrimPrefix(oldBinding.LastUsed.ArtifactDigest, "sha256:"))
			snapshot := encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: entries})
			ack, err := registry.Apply("snapshot", snapshot)
			if err != nil || len(ack.DrainingEngineIds) != 1 {
				t.Fatalf("missing drain acknowledgement: %+v %v", ack, err)
			}
			_, prepareErr := sessions.Prepare(ctx, "chat-one")
			if prepareErr == nil {
				t.Fatal("retired engine admitted during active prompt")
			}
			if action != "upgrade" && prepareErr.Error() != "engine_unavailable" {
				t.Fatalf("disabled/revoked engine must reject preparation before checking session occupancy: %v", prepareErr)
			}
			if host.Stats().Processes != 1 {
				t.Fatal("active process killed on revoke")
			}
			if _, err := os.Stat(oldSnapshot); err != nil {
				t.Fatal("active snapshot removed", err)
			}
			write(t, filepath.Join(root, "release-turn"), "finish", 0600)
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for host.Stats().Processes != 0 && ctx.Err() == nil {
				time.Sleep(10 * time.Millisecond)
			}
			if host.Stats().Processes != 0 {
				t.Fatal("retired process retained after prompt")
			}
			binding, err := store.LoadEngineSession(p.ChatID)
			if err != nil || binding.ContextState != "persisted" {
				t.Fatalf("lost completed binding: %+v %v", binding, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "runtime", "agent-engine-profiles", binding.ProfileID, "retained")); err != nil || string(data) != "profile" {
				t.Fatalf("profile lost: %v", err)
			}
			ack, err = registry.Apply("snapshot", snapshot)
			if err != nil || len(ack.DrainingEngineIds) != 0 {
				t.Fatalf("drain never completed: %+v %v", ack, err)
			}
			if action != "disable" {
				if _, err := os.Stat(oldSnapshot); !os.IsNotExist(err) {
					t.Fatal("retired snapshot retained", err)
				}
			}
			if action == "upgrade" {
				if _, err := sessions.Prepare(ctx, p.ChatID); err != nil {
					t.Fatal(err)
				}
				restored, _ := store.LoadEngineSession(p.ChatID)
				if restored.LastUsed.PluginVersion != "2.0.0" || restored.ACPSessionID != binding.ACPSessionID {
					t.Fatal("active upgrade lost context")
				}
			} else if _, err := sessions.Prepare(ctx, p.ChatID); err == nil {
				t.Fatal("disabled engine admitted after drain")
			}
		})
	}
}

func TestIncompatibleUpgradePreservesExternalBinding(t *testing.T) {
	registry, store, host, sessions, root := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := store.LoadEngineSession(p.ChatID)
	upgrade := sessionDelivery(t, root, strings.Replace(sessionAgent, "case 'session/new':", "case 'session/new':process.exit(71);break;case 'never':", 1), "fixture", "2.0.0", "install2")
	if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{upgrade}})); err != nil {
		t.Fatal(err)
	}
	for host.Stats().Processes != 0 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := sessions.Prepare(ctx, p.ChatID); err == nil || err.Error() != "session_restore_failed" {
		t.Fatalf("incompatible restore did not fail closed: %v", err)
	}
	after, _ := store.LoadEngineSession(p.ChatID)
	if string(encoded(t, before)) != string(encoded(t, after)) {
		t.Fatal("failed restore changed durable context")
	}
}

func TestBrokenCandidateNeverReplacesCurrentEngine(t *testing.T) {
	registry, _, _, sessions, root := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	upgrade := sessionDelivery(t, root, `process.exit(23)`, "fixture", "2.0.0", "install2")
	if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{upgrade}})); err == nil {
		t.Fatal("broken candidate replaced current")
	}
	if d := registry.Directory(); d.Revision != 1 || d.Engines[0].PluginVersion != "1.0.0" {
		t.Fatalf("failed upgrade published: %+v", d)
	}
	if _, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("still usable")}, nil); err != nil {
		t.Fatal(err)
	}
}

func sessionDelivery(t *testing.T, root, script, pluginID, version, installationRef string) dto.EngineInstallation {
	t.Helper()
	delivery := filepath.Join(root, "desktop", "data", "agent-engines")
	pkg := filepath.Join(delivery, installationRef, "package")
	if err := os.CopyFS(pkg, os.DirFS(filepath.Join(delivery, "install", "package"))); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Dir(pkg), 0700)
	data, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest dto.EnginePluginManifest
	json.Unmarshal(data, &manifest)
	manifest.ID = pluginID
	manifest.Desktop.BundleTopLevelDir = pluginID
	manifest.Version = version
	write(t, filepath.Join(pkg, "manifest.json"), string(encoded(t, manifest)), 0600)
	write(t, filepath.Join(pkg, "agent.cjs"), script, 0600)
	digest, err := connector.RuntimeFingerprint(pkg)
	if err != nil {
		t.Fatal(err)
	}
	var record dto.TrustedDeliveryRecord
	data, err = os.ReadFile(filepath.Join(delivery, "install", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(data, &record)
	record.InstallationRef = installationRef
	record.PluginID = pluginID
	record.PluginVersion = version
	record.ArtifactDigest = "sha256:" + digest
	record.Source.AuthorizationRef = "local-import:" + installationRef
	write(t, filepath.Join(delivery, installationRef, "record.json"), string(encoded(t, record)), 0600)
	return dto.EngineInstallation{EngineID: pluginID + "/agent", PluginID: pluginID, PluginVersion: version, InstallationRef: installationRef, ArtifactDigest: record.ArtifactDigest, Enabled: true}
}

func TestPluginLifecyclePreservesOtherActiveSession(t *testing.T) {
	for _, action := range []string{"upgrade", "disable", "revoke", "cancel"} {
		t.Run(action, func(t *testing.T) {
			script := strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':require('node:fs').writeFileSync(process.env.ZENMIND_AGENT_PROFILE+'/retained','profile');console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'started'}}}}));const timer=setInterval(()=>{if(require('node:fs').existsSync('release-turn')){clearInterval(timer);send(m.id,{stopReason:'end_turn'})}},10);break;`, 1)
			// A background worker and its child outlive the protocol turn until
			// the retired adapter tree is reaped; neither inherits its stdio.
			worker := `const {spawn}=require('node:child_process');const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore'});require('node:fs').writeFileSync('background-pids.json',JSON.stringify([process.pid,child.pid]));setInterval(()=>{},1000);`
			script = strings.Replace(script, "case 'session/prompt':", "case 'session/prompt':require('node:child_process').spawn(process.execPath,['-e',"+string(encoded(t, worker))+"],{stdio:'ignore',detached:true});", 1)
			script = strings.Replace(script, "case 'session/new':", "case 'session/resume':send(m.id,{configOptions:options()});break;case 'session/new':", 1)

			registry, store, host, _, root := sessionFixture(t, script)
			other := sessionDelivery(t, root, script, "other", "1.0.0", "other1")
			data, err := os.ReadFile(filepath.Join(root, "desktop/data/agent-engines/install/record.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record dto.TrustedDeliveryRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			original := dto.EngineInstallation{EngineID: "fixture/agent", PluginID: "fixture", PluginVersion: "1.0.0", InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: true}
			if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{original, other}})); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.EnsureChat("chat-two", "other", ""); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"project", "other"} {
				if err := os.MkdirAll(filepath.Join(root, key), 0700); err != nil {
					t.Fatal(err)
				}
			}
			sessions := driver.NewSessions(host, store, func(key string) (catalog.AgentDefinition, bool) {
				id := "fixture/agent"
				if key == "other" {
					id = "other/agent"
				}
				workspace := filepath.Join(root, key)
				return catalog.AgentDefinition{Key: key, Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": id, "workspaceRoot": workspace}, Workspace: catalog.AgentWorkspaceConfig{Root: workspace}}, true
			})
			defer sessions.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			first, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			second, err := sessions.Prepare(ctx, "chat-two")
			if err != nil {
				t.Fatal(err)
			}
			if action == "upgrade" {
				first, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: first.ChatID, PreparationID: first.PreparationID, ConfigRevision: first.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			second, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: second.ChatID, PreparationID: second.PreparationID, ConfigRevision: second.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
			if err != nil {
				t.Fatal(err)
			}
			targetCtx, cancelTarget := context.WithCancel(ctx)
			defer cancelTarget()
			finished := make([]chan error, 2)
			for i, p := range []dto.PreparedSession{first, second} {
				started := make(chan struct{}, 1)
				finished[i] = make(chan error, 1)
				promptCtx := ctx
				if i == 0 {
					promptCtx = targetCtx
				}
				go func() {
					_, err := sessions.Prompt(promptCtx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("wait")}, func(context.Context, driver.Incoming) (any, *sdk.RequestError) {
						started <- struct{}{}
						return nil, nil
					})
					finished[i] <- err
				}()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			background := make([][]int, 2)
			for i, key := range []string{"project", "other"} {
				for ctx.Err() == nil {
					data, err := os.ReadFile(filepath.Join(root, key, "background-pids.json"))
					if err == nil && json.Unmarshal(data, &background[i]) == nil && len(background[i]) == 2 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if len(background[i]) != 2 {
					t.Fatal("background tree did not start")
				}
			}
			t.Cleanup(func() {
				// Failure cleanup is limited to this fixture's recorded child groups.
				for _, pids := range background {
					exec.Command("/bin/kill", "-KILL", "--", strconv.Itoa(-pids[0])).Run()
				}
			})
			assertBackground := func(index int, alive bool) {
				t.Helper()
				for _, pid := range background[index] {
					running := exec.Command("/bin/kill", "-0", strconv.Itoa(pid)).Run() == nil
					if running != alive {
						t.Fatalf("plugin %d background pid %d alive=%t, want %t", index, pid, running, alive)
					}
				}
			}

			before, err := store.LoadEngineSession(second.ChatID)
			if err != nil {
				t.Fatal(err)
			}
			firstBinding, err := store.LoadEngineSession(first.ChatID)
			if err != nil {
				t.Fatal(err)
			}
			if before.ProfileID == firstBinding.ProfileID {
				t.Fatal("plugins share credential profile")
			}
			entries := []dto.EngineInstallation{other}
			switch action {
			case "upgrade":
				entries = append(entries, sessionDelivery(t, root, script, "fixture", "2.0.0", "install2"))
			case "disable":
				original.Enabled = false
				entries = append(entries, original)
			}
			ack, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 3, Entries: entries}))
			if err != nil {
				t.Fatal(err)
			}
			if len(ack.DrainingEngineIds) != 1 || ack.DrainingEngineIds[0] != "fixture/agent" {
				t.Fatalf("wrong plugin draining: %+v", ack)
			}
			if host.Stats().Processes != 2 {
				t.Fatal("lifecycle interrupted an active process")
			}
			assertBackground(0, true)
			assertBackground(1, true)
			select {
			case err := <-finished[1]:
				t.Fatalf("other active prompt ended: %v", err)
			default:
			}
			after, err := store.LoadEngineSession(second.ChatID)
			if err != nil || string(encoded(t, before)) != string(encoded(t, after)) {
				t.Fatalf("other binding changed: %v", err)
			}
			if action == "cancel" {
				cancelTarget()
			} else {
				write(t, filepath.Join(root, "project/release-turn"), "finish", 0600)
			}
			select {
			case err := <-finished[0]:
				if action == "cancel" && err == nil {
					t.Fatal("cancelled background turn succeeded")
				}
				if action != "cancel" && err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for host.Stats().Processes != 1 && ctx.Err() == nil {
				time.Sleep(10 * time.Millisecond)
			}
			if host.Stats().Processes != 1 {
				t.Fatal("only affected process should drain")
			}
			assertBackground(0, false)
			assertBackground(1, true)
			for _, e := range registry.Directory().Engines {
				if e.EngineID == other.EngineID && (!e.Enabled || e.RuntimeState != "busy") {
					t.Fatalf("other directory changed: %+v", e)
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "runtime/agent-engine-profiles", before.ProfileID, "retained")); err != nil || string(data) != "profile" {
				t.Fatalf("other profile lost: %v", err)
			}
			if action == "upgrade" {
				oldSnapshot := filepath.Join(root, "runtime/ru-agent-engines/fixture/agent", strings.TrimPrefix(firstBinding.LastUsed.ArtifactDigest, "sha256:"))
				if _, err := os.Stat(oldSnapshot); !os.IsNotExist(err) {
					t.Fatalf("drained version remains pinned: %v", err)
				}
				restored, err := sessions.Prepare(ctx, first.ChatID)
				if err != nil {
					t.Fatal(err)
				}
				binding, err := store.LoadEngineSession(first.ChatID)
				if err != nil {
					t.Fatal(err)
				}
				if binding.LastUsed.PluginVersion != "2.0.0" || binding.ACPSessionID != firstBinding.ACPSessionID || binding.ProfileID != firstBinding.ProfileID || string(encoded(t, binding.ConfigSelection)) != string(encoded(t, firstBinding.ConfigSelection)) {
					t.Fatalf("target failed to restore into new version: %+v", binding)
				}
				if _, err := sessions.Prompt(ctx, restored.ChatID, restored.PreparationID, restored.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("continue after upgrade")}, nil); err != nil {
					t.Fatal(err)
				}
				assertBackground(1, true)
				select {
				case err := <-finished[1]:
					t.Fatalf("target recovery interrupted other prompt: %v", err)
				default:
				}
				after, err := store.LoadEngineSession(second.ChatID)
				if err != nil || string(encoded(t, before)) != string(encoded(t, after)) {
					t.Fatalf("target recovery changed other binding: %v", err)
				}
			}
			write(t, filepath.Join(root, "other/release-turn"), "finish", 0600)
			select {
			case err := <-finished[1]:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			prepared, err := sessions.Prepare(ctx, second.ChatID)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded(t, prepared)) != string(encoded(t, second)) {
				t.Fatal("unaffected preparation/configuration changed")
			}
			if _, err := sessions.Prompt(ctx, prepared.ChatID, prepared.PreparationID, prepared.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("continue")}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigurationAdmissionUsesCategoryRatherThanProductID(t *testing.T) {
	script := strings.ReplaceAll(sessionAgent, "ModelID", "fast-mode")
	_, _, _, sessions, _ := sessionFixture(t, script)
	prepared, err := sessions.Prepare(context.Background(), "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.ConfigOptions) != 1 {
		t.Fatal("a model option was hidden by its ID", prepared.ConfigOptions)
	}
	_, err = sessions.Configure(context.Background(), dto.ConfigUpdate{ChatID: prepared.ChatID, PreparationID: prepared.PreparationID, ConfigRevision: prepared.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "fast-mode", Value: json.RawMessage(`"Model-B"`)}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrepareReclaimsUnusedSessionsAtProcessCapacity(t *testing.T) {
	_, store, host, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 12; i++ {
		id := "unused-" + strconv.Itoa(i)
		if _, _, err := store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Prepare(ctx, id); err != nil {
			t.Fatalf("preparation %d: %v", i, err)
		}
		if got := host.Stats().Processes; got > 4 {
			t.Fatalf("process limit exceeded: %d", got)
		}
	}
}

func TestReclaimedPreparationKeepsConfirmedConfiguration(t *testing.T) {
	_, store, _, sessions, _ := sessionFixture(t, sessionAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		id := "configured-" + strconv.Itoa(i)
		if _, _, err := store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: id, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		id := "configured-" + strconv.Itoa(i)
		if _, err := sessions.Inspect(id); err == nil {
			continue
		}
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(p.ConfigOptions[0]), `"currentValue":"Model-B"`) {
			t.Fatalf("recreated preparation lost configuration: %s", p.ConfigOptions)
		}
		return
	}
	t.Fatal("capacity did not reclaim any preparation")
}

func TestPreparationPressurePreservesActiveOperationsAndHistory(t *testing.T) {
	script := strings.Replace(sessionAgent,
		"model=m.params.value;send(m.id,{configOptions:options()});break;",
		`require('node:fs').writeFileSync('config-active','');const timer=setInterval(()=>{if(require('node:fs').existsSync('config-release')){clearInterval(timer);model=m.params.value;send(m.id,{configOptions:options()});}},10);break;`, 1)
	_, store, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var prepared []dto.PreparedSession
	for i := 0; i < 4; i++ {
		id := "protected-" + strconv.Itoa(i)
		if _, _, err := store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		prepared = append(prepared, p)
		if i < 3 {
			if _, err := sessions.Prompt(ctx, id, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("remember")}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := prepared[3]
	configured := make(chan error, 1)
	go func() {
		_, err := sessions.Configure(ctx, dto.ConfigUpdate{ChatID: p.ChatID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
		configured <- err
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "config-active")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := sessions.Prepare(ctx, "chat-one"); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("active configuration must not be reclaimed: %v", err)
	}
	write(t, filepath.Join(root, "config-release"), "", 0600)
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	p, err := sessions.Prepare(ctx, p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("remember")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prepare(ctx, "chat-one"); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("persisted sessions must not be reclaimed: %v", err)
	}
	for _, before := range prepared {
		after, err := sessions.Inspect(before.ChatID)
		if err != nil || after.PreparationID != before.PreparationID {
			t.Fatalf("protected session replaced: %s %v", before.ChatID, err)
		}
	}
	if host.Stats().Processes != 4 {
		t.Fatal("protected processes changed")
	}
}

func TestIdleTimerReleasesSessionsAndEventuallyTheirWarmWorker(t *testing.T) {
	_, _, host, sessions, _ := sessionFixtureWithOptions(t, multiplexAgent, driver.Options{IdleTimeout: 80 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := sessions.Inspect("chat-one"); err != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("idle session was never released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for host.Stats().Processes != 0 {
		if ctx.Err() != nil {
			t.Fatal("empty worker was never released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCapacityDoesNotEvictSessionsWithPossibleBackgroundTools(t *testing.T) {
	script := strings.Replace(multiplexAgent, "case 'session/prompt':send", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:p.sessionId,update:{sessionUpdate:'tool_call',toolCallId:'bg',title:'background work',kind:'execute',status:'completed'}}}));send`, 1)
	_, store, host, sessions, _ := sessionFixtureWithOptions(t, script, driver.Options{MaxSessions: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, id := range []string{"chat-one", "chat-two"} {
		store.EnsureChat(id, "project", "")
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = sessions.Prompt(ctx, id, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("run")}, nil); err != nil {
			t.Fatal(err)
		}
	}
	store.EnsureChat("chat-three", "project", "")
	if _, err := sessions.Prepare(ctx, "chat-three"); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("background work was evicted: %v", err)
	}
	for _, id := range []string{"chat-one", "chat-two"} {
		if _, err := sessions.Inspect(id); err != nil {
			t.Fatalf("lost pinned session: %v", err)
		}
	}
	if host.Stats().Processes != 1 {
		t.Fatal(host.Stats())
	}
}

func TestSharedWorkerRejectsDuplicateUpstreamSessionIdentity(t *testing.T) {
	script := strings.Replace(multiplexAgent, "'session-'+(++count)", "'duplicate-session'", 1)
	_, store, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureChat("chat-two", "project", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prepare(ctx, "chat-two"); err == nil {
		t.Fatal("two Chats accepted the same upstream identity")
	}
	if binding, _ := store.LoadEngineSession("chat-two"); binding != nil {
		t.Fatal("duplicate binding persisted")
	}
}

func TestSharedWorkerReloadsCredentialsAfterAuthFailureWithoutStoppingSibling(t *testing.T) {
	script := strings.Replace(multiplexAgent, "let count=0;", "let count=0;const authenticated=fs.existsSync('credentials-restored');", 1)
	script = strings.Replace(script, "case 'session/prompt':send", `case 'session/prompt':if(!authenticated){console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'auth required'}}));break;}send`, 1)
	script = strings.Replace(script, "case 'session/close':", `case 'session/resume':sessions.set(p.sessionId,'Model-A');send(m.id,{sessionId:p.sessionId,configOptions:options('Model-A')});break;case 'session/close':`, 1)
	_, store, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	first, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	store.EnsureChat("chat-two", "project", "")
	second, err := sessions.Prepare(ctx, "chat-two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, first.ChatID, first.PreparationID, first.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hello")}, nil); err == nil {
		t.Fatal("expected authentication failure")
	}
	write(t, filepath.Join(root, "credentials-restored"), "", 0600)
	restored, err := sessions.Prepare(ctx, first.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Prompt(ctx, restored.ChatID, restored.PreparationID, restored.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("retry")}, nil); err != nil {
		t.Fatalf("retry kept stale process credentials: %v", err)
	}
	if alive, err := sessions.Inspect(second.ChatID); err != nil || alive.PreparationID != second.PreparationID {
		t.Fatalf("auth failure stopped sibling: %v", err)
	}
	if host.Stats().Processes != 2 {
		t.Fatal(host.Stats())
	}
}

func TestCompletedReadToolsCanYieldCapacityButUnfinishedToolsCannot(t *testing.T) {
	for _, status := range []string{"completed", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			script := strings.Replace(multiplexAgent, "case 'session/prompt':send", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:p.sessionId,update:{sessionUpdate:'tool_call',toolCallId:'read',title:'read file',kind:'read',status:'`+status+`'}}}));send`, 1)
			_, store, _, sessions, _ := sessionFixtureWithOptions(t, script, driver.Options{MaxSessions: 1})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("read")}, nil); err != nil {
				t.Fatal(err)
			}
			store.EnsureChat("chat-two", "project", "")
			_, err = sessions.Prepare(ctx, "chat-two")
			if status == "completed" && err != nil {
				t.Fatalf("completed foreground read pinned forever: %v", err)
			}
			if status == "in_progress" && !errors.Is(err, driver.ErrBusy) {
				t.Fatalf("unfinished tool evicted: %v", err)
			}
		})
	}
}

func TestSharedWorkerUpgradeDrainsActiveChatAndUsesNewVersionForNewChats(t *testing.T) {
	script := strings.Replace(multiplexAgent, "'session-'+(++count)", "require('node:crypto').randomUUID()", 1)
	script = strings.Replace(script, "case 'session/close':", `case 'session/resume':sessions.set(p.sessionId,'Model-A');send(m.id,{sessionId:p.sessionId,configOptions:options('Model-A')});break;case 'session/close':`, 1)
	script = strings.Replace(script, "case 'session/prompt':send(m.id,{stopReason:'end_turn',model:sessions.get(p.sessionId),sessionId:p.sessionId});break;", `case 'session/prompt':fs.writeFileSync('active-prompt','');const timer=setInterval(()=>{if(fs.existsSync('release-prompt')){clearInterval(timer);send(m.id,{stopReason:'end_turn'});}},10);break;`, 1)
	registry, store, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	store.EnsureChat("chat-two", "project", "")
	if _, err = sessions.Prepare(ctx, "chat-two"); err != nil {
		t.Fatal(err)
	}
	if host.Stats().Processes != 1 {
		t.Fatal(host.Stats())
	}
	finished := make(chan error, 1)
	go func() {
		_, err := sessions.Prompt(ctx, first.ChatID, first.PreparationID, first.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("wait")}, nil)
		finished <- err
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "active-prompt")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	upgrade := sessionDelivery(t, root, script, "fixture", "2.0.0", "upgrade")
	if _, err = registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{upgrade}})); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		t.Fatalf("upgrade interrupted active Chat: %v", err)
	default:
	}
	store.EnsureChat("chat-three", "project", "")
	if _, err = sessions.Prepare(ctx, "chat-three"); err != nil {
		t.Fatal(err)
	}
	binding, _ := store.LoadEngineSession("chat-three")
	if binding.LastUsed.PluginVersion != "2.0.0" {
		t.Fatal("new Chat reused retired worker")
	}
	write(t, filepath.Join(root, "release-prompt"), "", 0600)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	restored, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if restored.PreparationID == first.PreparationID {
		t.Fatal("retired preparation survived upgrade")
	}
	binding, _ = store.LoadEngineSession("chat-one")
	if binding.LastUsed.PluginVersion != "2.0.0" || binding.ContextState != "persisted" {
		t.Fatal("did not restore original completed Chat")
	}
	for host.Stats().Processes != 1 {
		if ctx.Err() != nil {
			t.Fatal("retired shared worker never drained")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPromptQueueTimeoutDoesNotShortenSharedSessionCleanup(t *testing.T) {
	script := strings.Replace(multiplexAgent, "case 'session/close':sessions.delete(p.sessionId);send(m.id,{});break;", "case 'session/close':sessions.delete(p.sessionId);setTimeout(()=>send(m.id,{}),30);break;", 1)
	_, store, _, sessions, _ := sessionFixtureWithOptions(t, script, driver.Options{QueueTimeout: time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal(err)
	}
	store.EnsureChat("chat-two", "project", "")
	second, err := sessions.Prepare(ctx, "chat-two")
	if err != nil {
		t.Fatal(err)
	}
	if err = sessions.CloseChat("chat-one"); err != nil {
		t.Fatalf("prompt queue budget killed session cleanup: %v", err)
	}
	if _, err = sessions.Prompt(ctx, second.ChatID, second.PreparationID, second.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("alive")}, nil); err != nil {
		t.Fatalf("sibling stopped: %v", err)
	}
}

func TestConfigurationPreviewDoesNotReserveChatAndReusesWarmWorker(t *testing.T) {
	_, store, host, sessions, root := sessionFixture(t, multiplexAgent)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cached, err := sessions.Preview(ctx, "project", false)
	if err != nil || len(cached.ConfigOptions) != 0 || host.Stats().Processes != 0 {
		t.Fatalf("cache read started a process: %+v %v %+v", cached, err, host.Stats())
	}
	preview, err := sessions.Preview(ctx, "project", true)
	if err != nil || len(preview.ConfigOptions) != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if binding, err := store.LoadEngineSession("chat-one"); err != nil || binding != nil {
		t.Fatalf("preview reserved Chat: %+v %v", binding, err)
	}
	if _, err := sessions.Inspect("chat-one"); err == nil {
		t.Fatal("preview created resident Chat")
	}
	for range 5 {
		if _, err := sessions.Preview(ctx, "project", true); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prompt(ctx, prepared.ChatID, prepared.PreparationID, prepared.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hello")}, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "initializations")); err != nil || string(data) != "1\n" {
		t.Fatalf("preview did not reuse warm worker: %q %v", data, err)
	}
}

func TestConfigurationPreviewUsesSpareCapacityBesideResidentChat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   string
		capacity int
	}{
		{"shared-worker", multiplexAgent, 2},
		{"single-session-worker", sessionAgent, 2},
		{"full-host", multiplexAgent, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, host, sessions, _ := sessionFixtureWithOptions(t, tc.script, driver.Options{MaxProcesses: tc.capacity, MaxSessions: 2})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			p, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: p.ChatID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: "ModelID", Value: json.RawMessage(`"Model-B"`)}}})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := sessions.Preview(ctx, "project", true)
			if tc.capacity == 1 {
				if !errors.Is(err, driver.ErrBusy) {
					t.Fatalf("full host should defer discovery: %v", err)
				}
			} else {
				if err != nil || len(preview.ConfigOptions) != 1 || !strings.Contains(string(preview.ConfigOptions[0]), `"currentValue":"Model-A"`) {
					t.Fatalf("resident Chat blocked new-chat defaults despite spare capacity: %+v %v; %+v", preview, err, host.Stats())
				}
				cached, err := sessions.Preview(ctx, "project", false)
				if err != nil || len(cached.ConfigOptions) != 1 {
					t.Fatalf("preview did not populate new-chat configuration: %+v %v", cached, err)
				}
			}
			raw, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("unaffected")}, nil)
			if err != nil || !strings.Contains(string(raw), `"model":"Model-B"`) {
				t.Fatalf("preview changed resident Chat: %s %v", raw, err)
			}
		})
	}
}

func TestConfigurationPreviewCoalescesAndDoesNotEvictActiveChat(t *testing.T) {
	script := strings.Replace(multiplexAgent, "send(m.id,{sessionId:id,configOptions:options('Model-A')})", "fs.appendFileSync('new-sessions','1\\n');setTimeout(()=>send(m.id,{sessionId:id,configOptions:options('Model-A')}),150)", 1)
	_, _, host, sessions, root := sessionFixtureWithOptions(t, script, driver.Options{MaxProcesses: 1, MaxSessions: 1, MaxActive: 1})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	results := make(chan error, 8)
	for range 8 {
		go func() { _, err := sessions.Preview(ctx, "project", true); results <- err }()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "new-sessions")); err != nil || string(data) != "1\n" {
		t.Fatalf("duplicate probes: %q %v", data, err)
	}
	p, err := sessions.Prepare(ctx, "chat-one")
	if err != nil {
		t.Fatalf("preview leaked session slot: %v", err)
	}
	if host.Stats().Processes != 1 {
		t.Fatal(host.Stats())
	}
	// A different project scope cannot evict the resident Chat for discovery.
	other := driver.NewSessions(host, nil, func(key string) (catalog.AgentDefinition, bool) {
		return catalog.AgentDefinition{Key: key, Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}, true
	})
	defer other.Close()
	if _, err := other.Preview(ctx, "other-project", true); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("probe must defer at capacity: %v", err)
	}
	if _, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("unaffected")}, nil); err != nil {
		t.Fatalf("probe affected Chat: %v", err)
	}
}

func TestConfigurationPreviewInvalidatesOnUpgrade(t *testing.T) {
	registry, _, _, sessions, root := sessionFixture(t, multiplexAgent)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := sessions.Preview(ctx, "project", true); err != nil {
		t.Fatal(err)
	}
	upgrade := sessionDelivery(t, root, strings.ReplaceAll(multiplexAgent, "Model-A", "Model-C"), "fixture", "2.0.0", "preview-upgrade")
	if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{upgrade}})); err != nil {
		t.Fatal(err)
	}
	cached, err := sessions.Preview(ctx, "project", false)
	if err != nil || len(cached.ConfigOptions) != 0 {
		t.Fatalf("old options survived upgrade: %+v %v", cached, err)
	}
	fresh, err := sessions.Preview(ctx, "project", true)
	if err != nil || len(fresh.ConfigOptions) != 1 || !strings.Contains(string(fresh.ConfigOptions[0]), "Model-C") {
		t.Fatalf("new options missing: %+v %v", fresh, err)
	}
}
