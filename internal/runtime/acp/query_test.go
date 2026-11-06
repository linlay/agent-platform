package acp_test

import (
	"agent-platform/internal/agentengine"
	"agent-platform/internal/chat"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/contracts"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/knowledge/libraryinfo"
	"agent-platform/internal/llm"
	driver "agent-platform/internal/runtime/acp"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/orchestration"
	"agent-platform/internal/runtime/query"
	"agent-platform/internal/runtime/runstate"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/runtimeskills"
	"agent-platform/internal/stream"
	"agent-platform/internal/tools"
	sdk "github.com/coder/acp-go-sdk"
)

type queryCatalog struct {
	catalog.Registry
	def catalog.AgentDefinition
}

func (r queryCatalog) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	return r.def, key == r.def.Key
}
func (r queryCatalog) DefaultAgentKey() string          { return r.def.Key }
func (r queryCatalog) Agents(string) []api.AgentSummary { return nil }

func TestQueryUsesACPAndPersistsItsAnswer(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'ACP answer'}}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello", IncludeFullText: true}, runtimetypes.QueryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ACP answer" || result.FinishReason != "complete" {
		t.Fatalf("ACP query = %+v", result)
	}
	saved, err := store.Summary("chat-one")
	if err != nil || saved.LastRunID != result.RunID || saved.LastRunContent != "ACP answer" {
		t.Fatalf("answer not persisted: %+v %v", saved, err)
	}
}

func TestQuerySendsUploadedTextToACPWithoutTrustingClientPath(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(m.params.prompt)}}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	if err := os.MkdirAll(filepath.Join(store.ChatDir("chat-one"), "uploads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "uploads", "note.txt"), []byte("uploaded project fact"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "read attachment", References: []runtimetypes.Reference{{Type: "file", URL: "/api/resource?file=chat-one/uploads/note.txt", Path: "/etc/passwd", Name: "note.txt"}}}, runtimetypes.QueryHooks{})
	if err != nil || !strings.Contains(result.Content, "uploaded project fact") || strings.Contains(result.Content, "/etc/passwd") {
		t.Fatalf("attachment not translated: %+v %v", result, err)
	}
}

func TestQuerySendsScreenshotAsACPImageOnlyWhenNegotiated(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(m.params.prompt)}}}}));`, 1)
			if supported {
				script = strings.Replace(script, "loadSession:true", "loadSession:true,promptCapabilities:{image:true}", 1)
			}
			_, store, _, sessions, root := sessionFixture(t, script)
			var pngData bytes.Buffer
			if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(store.ChatDir("chat-one"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "screenshot.png"), pngData.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			result, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "see screenshot", References: []runtimetypes.Reference{{Type: "file", URL: "@chat/screenshot.png", MimeType: "text/plain"}}}, runtimetypes.QueryHooks{})
			if supported {
				if err != nil || !strings.Contains(result.Content, `"type":"image"`) || !strings.Contains(result.Content, `"mimeType":"image/png"`) {
					t.Fatalf("image missing: %+v %v", result, err)
				}
			} else if err == nil && !strings.Contains(result.ErrorMessage, "capability_unsupported") {
				t.Fatalf("unsupported image silently accepted: %+v", result)
			}
		})
	}
}

// A normal pasted screenshot already exceeds 1 MiB after ACP base64 encoding.
func TestQuerySendsLargeImageWithoutLosingConnection(t *testing.T) {
	for _, size := range []int{843408, 10 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			script := strings.Replace(sessionAgent, "loadSession:true", "loadSession:true,promptCapabilities:{image:true}", 1)
			script = strings.Replace(script, "case 'session/prompt':", `case 'session/prompt':{
const image=m.params.prompt.find(p=>p.type==='image');
const text=image ? String(Buffer.from(image.data,'base64').length) : 'next turn';
console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text}}}}));}
`, 1)
			_, store, host, sessions, root := sessionFixture(t, script)
			var pngData bytes.Buffer
			if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, size)
			copy(data, pngData.Bytes())
			if err := os.MkdirAll(store.ChatDir("chat-one"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "screenshot.png"), data, 0600); err != nil {
				t.Fatal(err)
			}
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "see screenshot", References: []runtimetypes.Reference{{Type: "file", URL: "@chat/screenshot.png"}}}
			result, err := service.ExecuteQuery(ctx, command, runtimetypes.QueryHooks{})
			if err != nil || result.Content != fmt.Sprint(size) || result.FinishReason != "complete" {
				t.Fatalf("large image did not reach ACP: %+v %v", result, err)
			}
			command.References = nil
			result, err = service.ExecuteQuery(ctx, command, runtimetypes.QueryHooks{})
			if err != nil || result.Content != "next turn" || host.Stats().Processes != 1 {
				t.Fatalf("session not reusable: %+v %v stats=%+v", result, err, host.Stats())
			}
		})
	}
}

func TestQueryDisclosesSelectedSkillFromTrustedCatalog(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(m.params.prompt.map(p=>p.type==='image'?{type:p.type,bytes:Buffer.from(p.data,'base64').length}:p))}}}}));`, 1)
	script = strings.Replace(script, "loadSession:true", "loadSession:true,promptCapabilities:{image:true}", 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	agentDir := filepath.Join(root, "ru-agents", "project", "revision")
	skillDir := filepath.Join(agentDir, "skills", "review")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review project\n---\nCheck the public interface first."), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := runtimeskills.Digest(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	skillRoot := runtimeskills.Root(filepath.Dir(filepath.Dir(agentDir)))
	if err := os.MkdirAll(skillRoot, 0700); err != nil {
		t.Fatal(err)
	}
	skillPath, _ := runtimeskills.Path(skillRoot, digest)
	if err := os.Rename(skillDir, skillPath); err != nil {
		t.Fatal(err)
	}
	if err := runtimeskills.WriteReferences(agentDir, []runtimeskills.Reference{{ID: "review", Digest: digest}}); err != nil {
		t.Fatal(err)
	}
	// Exercise the reported combination: a pasted image plus a selected skill.
	if err := os.MkdirAll(store.ChatDir("chat-one"), 0700); err != nil {
		t.Fatal(err)
	}
	imageData := make([]byte, 843408)
	copy(imageData, []byte("\x89PNG\r\n\x1a\n"))
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "screenshot.png"), imageData, 0600); err != nil {
		t.Fatal(err)
	}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, RuntimeDir: agentDir, Skills: []string{"review"}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	result, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "review this", References: []runtimetypes.Reference{{Type: "file", URL: "@chat/screenshot.png"}, {Type: "skill", ID: "review", Path: "/etc/passwd", Name: "forged"}}}, runtimetypes.QueryHooks{})
	if err != nil || !strings.Contains(result.Content, `"bytes":843408`) || !strings.Contains(result.Content, "Check the public interface first.") || strings.Contains(result.Content, "/etc/passwd") {
		t.Fatalf("skill not resolved: %+v %v", result, err)
	}
}

type contextToolExecutor struct {
	calls int
	write bool
	fail  bool
}

func (e *contextToolExecutor) Definitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{{Name: "fixture_lookup", Description: "Read a mounted connector", Parameters: map[string]any{"type": "object"}, Meta: map[string]any{"readOnly": !e.write}}}
}
func (e *contextToolExecutor) Invoke(_ context.Context, name string, args map[string]any, execution *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	if name != "fixture_lookup" || execution.Session.ChatID != "chat-one" || execution.Session.RunID == "" || args["key"] != "project" {
		return contracts.ToolExecutionResult{}, fmt.Errorf("wrong execution scope")
	}
	if e.write && !contracts.ConsumeToolApproval(execution, contracts.ToolApprovalFingerprint(execution, name, "write", "fixture")) {
		return contracts.ToolExecutionResult{}, fmt.Errorf("exact approval missing")
	}
	e.calls++
	if e.fail {
		return contracts.ToolExecutionResult{Output: "lookup failed", Error: "upstream unavailable", MCPResult: map[string]any{"privateTrace": "must-not-reach-model"}}, nil
	}
	return contracts.ToolExecutionResult{Output: "connector answer"}, nil
}

func (e *contextToolExecutor) PrepareToolApproval(_ context.Context, name string, _ map[string]any, exec *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	if !e.write {
		return nil, nil
	}
	return &contracts.ToolApproval{Fingerprint: contracts.ToolApprovalFingerprint(exec, name, "write", "fixture"), Title: "Change project record"}, nil
}

func platformToolAgent() string {
	script := "let bridge;" + strings.Replace(sessionAgent, "on('line',line=>", "on('line',async line=>", 1)
	script = strings.Replace(script, "loadSession:true", "loadSession:true,mcpCapabilities:{http:true}", 1)
	script = strings.Replace(script, "case 'session/new':", "case 'session/new':bridge=m.params.mcpServers[0];", 1)
	script = strings.Replace(script, "case 'session/prompt':", `case 'session/prompt':if(!bridge){console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32603,message:'missing MCP bridge'}}));break;} const res=await fetch(bridge.url,{method:'POST',headers:{'Content-Type':'application/json','Accept':'application/json, text/event-stream',...Object.fromEntries(bridge.headers.map(h=>[h.name,h.value]))},body:JSON.stringify({jsonrpc:'2.0',id:9,method:'tools/call',params:{name:'platform_call',arguments:{name:'fixture_lookup',arguments:{key:'project'}}}})});const response=await res.json();console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(response)}}}}));`, 1)
	return script
}

func TestQueryUsesStandardMCPToCallMountedPlatformTool(t *testing.T) {
	script := platformToolAgent()
	_, store, _, sessions, root := sessionFixture(t, script)
	executor := &contextToolExecutor{}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Tools: []string{"fixture_lookup"}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Tools: executor, Sessions: session.New(session.Dependencies{Chats: store, Tools: executor}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "use connector"}, runtimetypes.QueryHooks{})
	if err != nil || executor.calls != 1 || !strings.Contains(result.Content, "connector answer") {
		t.Fatalf("MCP tool not connected: calls=%d result=%+v err=%v", executor.calls, result, err)
	}
}

func TestACPToolFailurePreservesModelOutputAndError(t *testing.T) {
	_, store, _, sessions, root := sessionFixture(t, platformToolAgent())
	executor := &contextToolExecutor{fail: true}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Tools: []string{"fixture_lookup"}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Tools: executor, Sessions: session.New(session.Dependencies{Chats: store, Tools: executor}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	result, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "use connector"}, runtimetypes.QueryHooks{})
	if err != nil || !strings.Contains(result.Content, `"isError":true`) || !strings.Contains(result.Content, "lookup failed") || strings.Contains(result.Content, "must-not-reach-model") {
		t.Fatalf("MCP result=%+v error=%v", result, err)
	}
}

func TestQueryRejectsUnsupportedACPInputsBeforeSpawn(t *testing.T) {
	_, store, host, sessions, root := sessionFixture(t, sessionAgent)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	for _, cmd := range []runtimetypes.QueryCommand{
		{Lane: "btw"}, {Lane: "explain"}, {References: []runtimetypes.Reference{{Type: "chat", ID: "other"}}},
		{AccessLevel: "full_access"}, {AccessLevel: "auto_approve"}, {Params: map[string]any{"cwd": "/"}},
	} {
		cmd.ChatID, cmd.AgentKey, cmd.Message = "chat-one", "project", "hello"
		_, err := service.ExecuteQuery(context.Background(), cmd, runtimetypes.QueryHooks{})
		var request *runtimetypes.RequestError
		if !errors.As(err, &request) || request.Code != "capability_unsupported" {
			t.Errorf("unsupported %+v: %v", cmd, err)
		}
	}
	_, err := service.Execute(context.Background(), runtimetypes.ExecuteCommand{AgentKey: "project", Message: "execute", OutputSchema: map[string]any{"type": "object"}})
	var request *runtimetypes.RequestError
	if !errors.As(err, &request) || request.Code != "execute_unsupported_agent" {
		t.Errorf("execute: %v", err)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("unsupported entry spawned a process")
	}
}

func TestQueryUsesExternalModelWithoutNativeRegistry(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:model}}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}, ModelKey: "native-default"}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, model := range []string{"Model-B", ""} {
		cmd := runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"}
		if model != "" {
			cmd.Model = &runtimetypes.QueryModelOptions{Key: model}
		}
		result, err := service.ExecuteQuery(ctx, cmd, runtimetypes.QueryHooks{})
		if err != nil || result.Content != "Model-B" {
			t.Fatalf("external model %q: %+v %v", model, result, err)
		}
	}
	_, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello", Model: &runtimetypes.QueryModelOptions{Key: "model-b"}}, runtimetypes.QueryHooks{})
	var request *runtimetypes.RequestError
	if !errors.As(err, &request) || request.Code != "invalid_config_selection" {
		t.Fatalf("case changed: %v", err)
	}
}

func TestDetachedACPQueryReplaysAndCancelsWithoutNativeControls(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'partial'}}}}));break;`, 1)
	_, store, host, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		select {
		case e := <-sub.Events:
			if e.Type == "content.delta" {
				goto detached
			}
		case <-ctx.Done():
			t.Fatal("no streamed content")
		}
	}
detached:
	sub.Close()
	steer, err := service.Steer(ctx, runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, AgentKey: "project"}, Message: "ignored?"})
	if err == nil && steer.Accepted {
		t.Error("ACP accepted a Native steer queue")
	}
	level, err := service.SetAccessLevel(ctx, runtimetypes.AccessLevelCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, AgentKey: "project"}, AccessLevel: "full_access"})
	if err == nil && level.Accepted {
		t.Error("ACP falsely acknowledged permission change")
	}
	replay, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if _, err = service.Interrupt(ctx, runtimetypes.InterruptCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, AgentKey: "project"}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for {
		select {
		case e, ok := <-replay.Events:
			if !ok {
				goto finished
			}
			seen[e.Type]++
		case <-ctx.Done():
			t.Fatal("cancel did not close stream")
		}
	}
finished:
	replay.Close()
	if seen["content.delta"] != 1 || seen["run.cancel"] != 1 {
		t.Errorf("replay/cancel events=%v", seen)
	}
	for {
		saved, _ := store.Summary("chat-one")
		if saved != nil && saved.LastRunID == handle.RunID && saved.CanContinue && host.Stats().Processes == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cancellation not persisted: %+v processes=%d", saved, host.Stats().Processes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestACPChildUsesIsolatedSessionAndReturnsResult(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'child answer'}}}}));`, 1)
	script = strings.Replace(script, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'plan',entries:[{content:'child work',status:'completed'}]}}}));`, 1)
	script = strings.Replace(script, "stopReason:'end_turn'", "stopReason:m.params.prompt[0].text==='cancel child'?'cancelled':'end_turn'", 1)
	_, store, host, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}, VisibilityScopes: []string{"invoke"}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, ACP: sessions, Sessions: session.New(session.Dependencies{Chats: store})})
	summary, _ := store.Summary("chat-one")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	coordinator := orchestration.Coordinator{RunCtx: ctx, Registry: registry, Chats: store, Summary: *summary, Session: contracts.QuerySession{RunID: "parent-run", ChatID: "chat-one", AgentKey: "parent", RunOwner: contracts.AgentRunOwner("parent"), AccessLevel: "default"}, Agent: service, BuildQuerySession: service.BuildQuerySession, Mapper: llm.NewDeltaMapper("parent-run", "chat-one", contracts.Budget{}, nil, nil)}
	task := orchestration.PreparedSubTask{Spec: contracts.SubAgentTaskSpec{TaskAgentKey: "project", TaskText: "child work"}, TaskID: "task-one", SubTaskID: "sub-one", RequestID: "child-request", MainToolID: "invoke"}
	var childPlan stream.PlanUpdate
	result := coordinator.RunChildTaskWithOptions(0, task, nil, func(input stream.StreamInput) {
		if plan, ok := input.(stream.PlanUpdate); ok {
			childPlan = plan
		}
	}, orchestration.ChildRunOptions{})
	if childPlan.TaskID != task.TaskID || !strings.HasPrefix(childPlan.PlanID, task.TaskID) {
		t.Errorf("child plan lost attribution: %+v", childPlan)
	}
	if result.Status != "completed" || result.Text != "child answer" {
		t.Fatalf("child=%+v", result)
	}
	if binding, err := store.LoadEngineSession("chat-one"); err != nil || binding != nil {
		t.Fatalf("child replaced parent binding: %+v %v", binding, err)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("child retained process after result")
	}
	task.Spec.TaskText = "cancel child"
	cancelled := coordinator.RunChildTaskWithOptions(1, task, nil, func(stream.StreamInput) {}, orchestration.ChildRunOptions{})
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancelled child=%+v", cancelled)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("cancelled child retained process")
	}

}

func TestACPEventsUseExistingToolPlanAndUsageContracts(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':
 const update=(update)=>console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update}}));
 update({sessionUpdate:'agent_thought_chunk',content:{type:'text',text:'thinking'}});
 update({sessionUpdate:'tool_call',toolCallId:'external-ID',kind:'edit',status:'in_progress',rawInput:{file:'a.txt'}});
 update({sessionUpdate:'tool_call_update',toolCallId:'external-ID',status:'completed',content:[{type:'diff',path:'a.txt',oldText:'before',newText:'after'}],rawOutput:{done:true}});
 update({sessionUpdate:'plan',entries:[{content:'edit file',priority:'high',status:'completed'}]});
 update({sessionUpdate:'usage_update',used:123,size:1000,cost:{amount:0.2,currency:'USD'}});
 update({sessionUpdate:'agent_message_chunk',content:{type:'text',text:'done'}});`, 1)
	script = strings.Replace(script, "stopReason:'end_turn',sessionId:m.params.sessionId,model", "stopReason:'end_turn',usage:{inputTokens:15,outputTokens:-1,_meta:{secret:'not-for-display'}}", 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	result, err := service.ExecuteQuery(context.Background(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello", IncludeUsage: true}, runtimetypes.QueryHooks{})
	if err != nil || result.Content != "done" {
		t.Fatalf("query=%+v %v", result, err)
	}
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Plan == nil || len(detail.Plan.Tasks) != 1 || detail.Plan.Tasks[0].Description != "edit file" {
		t.Fatalf("plan must replay through existing snapshot: %+v", detail.Plan)
	}

	seen := map[string]bool{}
	for _, event := range detail.Events {
		seen[event.Type] = true
		if event.Type == "request.query" && event.Value("editingMode") == true {
			t.Error("external query advertises an unconfirmed Native editing mode")
		}
		if event.Type == "tool.result" {
			if event.String("toolId") != "external-ID" || event.String("toolName") != "acp/edit" || event.String("toolLabel") == "" {
				t.Errorf("tool identity=%+v", event)
			}
		}
		if event.Type == "tool.snapshot" {
			change, _ := event.Value("fileChange").(map[string]any)
			if change["lineStats"] == nil {
				t.Errorf("file diff lacks shared lineStats: %+v", change)
			}
		}
		if event.Type == "plan.update" {
			if event.Value("source") != nil {
				t.Fatal("ACP plan marker leaked")
			}
			entries, _ := event.Value("plan").([]any)
			if len(entries) != 1 {
				t.Errorf("plan must use existing array contract: %+v", event)
			} else if item, _ := entries[0].(map[string]any); item["taskId"] == "" || item["description"] != "edit file" {
				t.Errorf("plan item=%+v", item)
			}
		}
		if event.Type == "usage.snapshot" {
			usage, _ := event.Value("usage").(map[string]any)
			if usage["source"] == "acp" || usage["scope"] != nil || usage["values"] != nil {
				t.Fatalf("nonstandard usage: %+v", event)
			}
			raw, _ := json.Marshal(usage)
			if strings.Contains(string(raw), "not-for-display") || strings.Contains(string(raw), `"outputTokens":-1`) {
				t.Fatalf("invalid usage: %s", raw)
			}
		}

	}
	for _, kind := range []string{"reasoning.snapshot", "tool.result"} {
		if !seen[kind] {
			t.Errorf("missing %s: %v", kind, seen)
		}
	}
}

func TestMalformedACPEventFailsInsteadOfCancelling(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'tool_call',status:'completed'}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"}, runtimetypes.QueryHooks{})
	if err != nil || result.FinishReason != "error" || result.ErrorPayload["code"] != "rpc_connection_lost" {
		t.Fatalf("malformed event=%+v %v", result, err)
	}
}

func TestChatStartUsesACPAndKeepsParentControlBoundary(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:model}}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	runs := runstate.NewManager()
	runs.Register(context.Background(), contracts.QuerySession{RunID: "parent-run", ChatID: "parent-chat", AgentKey: "parent", AccessLevel: "default"})
	defer runs.Finish("parent-run")
	scope := controlscope.Scope{Transport: "http", Subject: "owner"}
	runs.BindRunControlScope("parent-run", scope)
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runs, Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started, err := service.StartRun(ctx, contracts.RunStartRequest{AgentKey: "project", ChatID: "chat-one", Message: "hello", ModelKey: "Model-B", Origin: contracts.RunOrigin{RunID: "parent-run", AgentKey: "parent", ToolID: "start", Subject: "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := runs.RunControlScope(started.RunID); !ok || got != scope {
		t.Fatalf("control scope=%+v %v", got, ok)
	}
	sub, err := service.AttachRun(controlscope.WithContext(ctx, scope), runtimetypes.RunRef{RunID: started.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		select {
		case _, ok := <-sub.Events:
			if !ok {
				goto completed
			}
		case <-ctx.Done():
			t.Fatal("chat_start did not finish")
		}
	}
completed:
	saved, err := store.Summary("chat-one")
	if err != nil || saved.LastRunContent != "Model-B" {
		t.Fatalf("chat_start=%+v %v", saved, err)
	}
}

func TestACPAutomationNeverApprovesOrReplaysInteraction(t *testing.T) {
	for _, tc := range []struct{ name, script, code string }{
		{"preauthenticated", strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'automation answer'}}}}));`, 1), ""},
		{"auth required", strings.Replace(sessionAgent, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", `case 'session/new':console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));break;`, 1), "auth_required"},
		{"prompt auth required", strings.Replace(sessionAgent, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));break;`, 1), "auth_required"},
		{"interaction required", strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',id:'permission',method:'session/request_permission',params:{sessionId:m.params.sessionId,toolCall:{toolCallId:'tool'},options:[{optionId:'allow',name:'Allow',kind:'allow_once'}]}}));`, 1), "interaction_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, _, sessions, root := sessionFixture(t, tc.script)
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "scheduled", Origin: &queryinput.Origin{Type: queryinput.OriginAutomation, AutomationID: "schedule-one"}}, runtimetypes.QueryHooks{})
			if tc.name == "auth required" {
				var request *runtimetypes.RequestError
				if !errors.As(err, &request) || request.Code != tc.code {
					t.Fatalf("auth=%+v %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" {
				if result.FinishReason != "complete" || result.Content != "automation answer" {
					t.Fatalf("automation=%+v", result)
				}
			} else if result.FinishReason != "error" || result.ErrorPayload["code"] != tc.code {
				t.Fatalf("interaction=%+v", result)
			}
		})
	}
}

func TestMissingACPEngineIsRejectedBeforeRunOrSpawn(t *testing.T) {
	registry, store, host, sessions, root := sessionFixture(t, sessionAgent)
	if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{}})); err != nil {
		t.Fatal(err)
	}
	catalogRegistry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	for _, configured := range []*driver.Sessions{nil, sessions} {
		service := query.NewService(query.Dependencies{Chats: store, Registry: catalogRegistry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: configured, DeltaMappers: llm.DeltaMapperFactory{}})
		_, err := service.StartQuery(context.Background(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"})
		var request *runtimetypes.RequestError
		if !errors.As(err, &request) || request.Code != "engine_unavailable" {
			t.Errorf("unavailable=%v", err)
		}
	}
	if host.Stats().Processes != 0 {
		t.Fatal("unavailable engine spawned")
	}
}

func TestQueryRestoresACPWithoutReplayingHistory(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/resume':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'old history'}}}}));send(m.id,{configOptions:options()});break;
 case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:model}}}}));`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		cmd := runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"}
		if i == 0 {
			cmd.Model = &runtimetypes.QueryModelOptions{Key: "Model-B"}
		}
		result, err := service.ExecuteQuery(ctx, cmd, runtimetypes.QueryHooks{})
		if err != nil || result.Content != "Model-B" {
			t.Fatalf("query %d=%+v %v", i, result, err)
		}
		sessions.CloseChat("chat-one")
	}
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range detail.Events {
		if e.Type == "content.snapshot" {
			count++
			if e.String("text") != "Model-B" {
				t.Fatalf("history replayed: %+v", e)
			}
		}
	}
	if count != 2 {
		t.Fatalf("persisted %d answers", count)
	}
}

func TestCodexTerminalOutputStreamsAndSurvivesReplay(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':
 const update=(update)=>console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update}}));
 update({sessionUpdate:'tool_call',toolCallId:'command',kind:'execute',status:'in_progress',content:[{type:'terminal',terminalId:'command'}]});
 update({sessionUpdate:'tool_call_update',toolCallId:'command',_meta:{terminal_output_delta:{terminal_id:'other',data:'WRONG'}}});
 update({sessionUpdate:'tool_call_update',toolCallId:'command',_meta:{terminal_output_delta:{terminal_id:'command',data:'Operation not '}}});
 update({sessionUpdate:'tool_call_update',toolCallId:'command',status:'completed',_meta:{terminal_output_delta:{terminal_id:'command',data:'permitted\n'},terminal_exit:{terminal_id:'command',exit_code:1}}});`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "run sandbox probe"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for i, want := range []string{"Operation not ", "permitted\n"} {
		event := awaitEvent(t, ctx, sub.Events, "tool.output")
		if event.String("delta") != want || event.String("toolId") != "command" {
			t.Fatalf("chunk %d: %+v", i, event)
		}
	}
	awaitEvent(t, ctx, sub.Events, "run.complete")
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range detail.Events {
		if event.Type != "tool.result" {
			continue
		}
		result := event.String("result")
		if !strings.Contains(result, "Operation not permitted") || !strings.Contains(result, `"exitCode":1`) || strings.Contains(result, "WRONG") {
			t.Fatalf("terminal result lost or mixed output: %s", result)
		}
		return
	}
	t.Fatal("missing persisted terminal result")
}

func TestACPCompleteUsageUsesExistingRunTotals(t *testing.T) {
	script := strings.Replace(sessionAgent, "stopReason:'end_turn',sessionId:m.params.sessionId,model", "stopReason:'end_turn',usage:{inputTokens:15,outputTokens:3,totalTokens:18,thoughtTokens:1,cachedReadTokens:5}", 1)
	script = strings.Replace(script, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'answer'}}}}));`, 1)

	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	result, err := service.ExecuteQuery(context.Background(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello", IncludeUsage: true}, runtimetypes.QueryHooks{})
	if err != nil || result.Usage.PromptTokens != 15 || result.Usage.CompletionTokens != 3 || result.Usage.TotalTokens != 18 {
		t.Fatalf("standard totals=%+v %v", result, err)
	}
	detail, err := store.LoadChat("chat-one")
	if err != nil || detail.ReplayUsage.LastRun.TotalTokens != 18 {
		t.Fatalf("replayed totals=%+v %v", detail, err)
	}
}

func TestExecutablePluginUsesOrdinaryQueryWithoutNode(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	_, store, host, sessions, root := sessionFixture(t, string(binary), func(engine *dto.EngineResource) {
		engine.Launch = dto.EngineLaunch{Runtime: "executable", Entry: "bin/agent.exe", Args: []string{"-test.run=^TestExecutableAgentFixture$", "--", "literal argument with spaces"}}
		engine.Dependencies = []dto.EngineDependency{}
	})
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "hello"}, runtimetypes.QueryHooks{})
	if err != nil || result.Content != "Executable ACP answer" {
		t.Fatalf("query=%+v %v", result, err)
	}
	saved, err := store.Summary("chat-one")
	if err != nil || saved.LastRunContent != result.Content {
		t.Fatalf("history=%+v %v", saved, err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("executable process was not drained")
	}
}

func TestFailedACPToolPreservesDetailsInOrdinaryHistory(t *testing.T) {
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':
 const update=(update)=>console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update}}));
 update({sessionUpdate:'tool_call',toolCallId:'search-1',kind:'search',title:'Search',status:'in_progress',rawInput:{query:''}});
 update({sessionUpdate:'tool_call_update',toolCallId:'search-1',status:'failed',rawInput:{query:'fixture weather'},rawOutput:{reason:'upstream unavailable'},content:[{type:'content',content:{type:'text',text:'Search service unavailable'}}]});
 update({sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Unable to retrieve weather'}});`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	result, err := service.ExecuteQuery(context.Background(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "weather"}, runtimetypes.QueryHooks{})
	if err != nil || result.Content != "Unable to retrieve weather" {
		t.Fatalf("query=%+v %v", result, err)
	}
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range detail.Events {
		if event.Type != "tool.result" {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(event.String("result")), &value); err != nil {
			t.Fatalf("invalid tool result: %v", err)
		}
		message, _ := value["error"].(string)
		if event.String("toolName") != "acp/search" || message == "" {
			t.Fatalf("missing ordinary tool failure: %+v", event)
		}
		output, _ := value["output"].(string)
		for _, expected := range []string{"fixture weather", "upstream unavailable", "Search service unavailable"} {
			if !strings.Contains(output, expected) {
				t.Fatalf("tool detail lost %q: %+v", expected, value)
			}
		}
		return
	}
	t.Fatal("missing tool.result in history")
}

func TestStandaloneInstalledExecutableUsesQueryAndRestoresChat(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := dto.EnginePluginManifest{PluginAPIVersion: 2, ID: "standalone", Name: "Standalone", Description: "Fixture", Version: "1.0.0", Platform: dto.PlatformTarget{Os: runtime.GOOS, Arch: runtime.GOARCH}}
	manifest.Desktop.BundleTopLevelDir = manifest.ID
	manifest.Requires.PlatformAgentEngineAPI = 2
	manifest.Requires.WebclientAgentEngineAPI = 1
	manifest.Resources.AgentEngines = []dto.EngineResource{{ID: "agent", Name: "Standalone", Protocol: "acp", Transport: "stdio", Launch: dto.EngineLaunch{Runtime: "executable", Entry: "bin/agent.exe", Args: []string{"-test.run=^TestExecutableAgentFixture$", "--", "literal argument with spaces"}}, Dependencies: []dto.EngineDependency{}}}
	metadata, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range map[string][]byte{"standalone/manifest.json": metadata, "standalone/bin/agent.exe": binary} {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0755)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "standalone/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	for round := 0; round < 2; round++ {
		engines, err := agentengine.NewStandalone(root, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		host, err := driver.New(engines, root, driver.Options{})
		if err != nil {
			t.Fatal(err)
		}
		sessions := driver.NewSessions(host, store, registry.AgentDefinition)
		if err = engines.Restore(); err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			archive := bytes.NewReader(buffer.Bytes())
			if _, err = engines.InstallArchive(archive, archive.Size(), false); err != nil {
				t.Fatal(err)
			}
		}
		service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "standalone-chat", AgentKey: "project", Message: "hello"}, runtimetypes.QueryHooks{})
		cancel()
		if err != nil || result.Content != "Executable ACP answer" {
			t.Fatalf("round %d: %+v %v", round, result, err)
		}
		binding, err := store.LoadEngineSession("standalone-chat")
		if err != nil || binding.ACPSessionID != "native-session" {
			t.Fatalf("binding=%+v %v", binding, err)
		}
		if err = sessions.Close(); err != nil {
			t.Fatal(err)
		}
		if err = host.Close(); err != nil {
			t.Fatal(err)
		}
		if err = engines.Close(); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := store.LoadChat("standalone-chat")
	if err != nil {
		t.Fatal(err)
	}
	answers := 0
	for _, event := range detail.Events {
		if event.Type == "content.snapshot" && event.String("text") == "Executable ACP answer" {
			answers++
		}
	}
	if answers != 2 {
		t.Fatalf("expected two persisted answers, got %d", answers)
	}
}

func TestPlatformMCPWriteRequiresExistingApproval(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			_, store, _, sessions, root := sessionFixture(t, platformToolAgent())
			executor := &contextToolExecutor{write: true}
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Tools: []string{"fixture_lookup"}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Tools: executor, Sessions: session.New(session.Dependencies{Chats: store, Tools: executor}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			ctx = runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: "owner"})
			handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "use connector"})
			if err != nil {
				t.Fatal(err)
			}
			sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
			approvals := ask.Value("approvals").([]any)
			approval := approvals[0].(map[string]any)
			if executor.calls != 0 {
				t.Fatal("executed before approval")
			}
			result, err := service.Submit(ctx, runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "decision", Params: []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": decision})}})
			if err != nil || !result.Accepted {
				t.Fatalf("submit=%+v %v", result, err)
			}
			awaitEvent(t, ctx, sub.Events, "run.complete")
			want := 0
			if decision == "approve" {
				want = 1
			}
			if executor.calls != want {
				t.Fatalf("calls=%d want=%d", executor.calls, want)
			}
		})
	}
}

type acpKnowledge struct {
	knowledge.ToolService
	called bool
}

func (k *acpKnowledge) Search(_ context.Context, id, q string, _ knowledge.SearchOptions) (knowledge.SearchResult, error) {
	if id != "docs" || q != "project" {
		return knowledge.SearchResult{}, fmt.Errorf("wrong query")
	}
	k.called = true
	return knowledge.SearchResult{LibraryID: id, Query: q, Count: 1, Results: []knowledge.SearchHit{{Path: "guide.md", Snippet: "knowledge evidence"}}}, nil
}

type knowledgeExecutor struct{ *knowledge.ToolHandler }

func (*knowledgeExecutor) Definitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{{Name: "kbase_search", Parameters: map[string]any{"type": "object"}, Meta: map[string]any{"readOnly": true}}}
}
func TestACPSelectedKnowledgeUsesExistingRetrieval(t *testing.T) {
	script := strings.ReplaceAll(platformToolAgent(), "fixture_lookup", "kbase_search")
	script = strings.Replace(script, "arguments:{key:'project'}", "arguments:{libraryId:'docs',query:'project'}", 1)
	script = strings.Replace(script, "const res=await fetch", "if(!JSON.stringify(m.params.prompt).includes('trusted connector instructions')) throw Error('connector skill missing');const res=await fetch", 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("trusted connector instructions"), 0600)
	backend := &acpKnowledge{}
	executor, routerErr := tools.NewToolRouter(&knowledgeExecutor{knowledge.NewToolHandler(backend)}, nil, nil, nil)
	if routerErr != nil {
		t.Fatal(routerErr)
	}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Tools: []string{"kbase_search"}, ConnectorSkills: []catalog.ConnectorSkill{{ID: "kbase-control", RuntimeDir: root}}, Connectors: []string{connector.KBaseControlConnectorID}, ConnectorNativeTools: []string{"kbase_search"}, ConnectorMounts: []catalog.ConnectorMount{{ID: connector.KBaseControlConnectorID, Dir: root}}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	builder := session.New(session.Dependencies{Chats: store, Tools: executor, KnowledgeLibrary: func(id string) (libraryinfo.LibraryOption, error) {
		return libraryinfo.LibraryOption{ID: id, Name: "Trusted docs", Readable: true}, nil
	}})
	service := query.NewService(query.Dependencies{Config: config.Config{Paths: config.PathsConfig{StateDir: t.TempDir()}}, Chats: store, Registry: registry, Runs: runstate.NewManager(), Tools: executor, Sessions: builder, ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "search docs", References: []runtimetypes.Reference{{Type: "kbase", ID: "docs", Name: "forged"}}}, runtimetypes.QueryHooks{})
	if err != nil || !backend.called || !strings.Contains(result.Content, "knowledge evidence") {
		t.Fatalf("retrieval=%+v err=%v called=%v", result, err, backend.called)
	}
}

func TestACPConnectorCLIReusesInstalledEntryWithoutDuplicateApproval(t *testing.T) {
	for _, mode := range []string{"none", "oneid"} {
		t.Run(mode, func(t *testing.T) { testACPConnectorCLI(t, mode, "automatic") })
	}
}

func TestACPConnectorCLIHonorsEngineApprovalAndMountBoundary(t *testing.T) {
	for _, scenario := range []string{"approve", "reject", "unmounted"} {
		t.Run(scenario, func(t *testing.T) { testACPConnectorCLI(t, "none", scenario) })
	}
}

func testACPConnectorCLI(t *testing.T, mode, scenario string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(filepath.Join(dir, "bin"), 0700)
	files := map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"none"}`, "cli.json": `{"versionCheck":{"command":{"darwin":"demo --version","linux":"demo --version","win32":"demo.exe --version"},"minVersion":"1.0.0"}}`, "bin/demo": "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0.0; else printf '%s' \"$1\"; fi\n"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	identity := ""
	if mode == "oneid" {
		files["connector.json"] = strings.Replace(files["connector.json"], `"authMode":"none"`, `"authMode":"oneid"`, 1)
		os.WriteFile(filepath.Join(dir, "connector.json"), []byte(files["connector.json"]), 0600)
		identity = filepath.Join(t.TempDir(), "identity")
		os.WriteFile(identity, []byte("synthetic-identity"), 0600)
		// The real CLI gets the identity; no prompt or MCP result should contain it.
		os.WriteFile(filepath.Join(dir, "bin", "demo"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0.0; else [ \"$AP_ID_TOKEN\" = synthetic-identity ] || exit 8; printf '%s' \"$1\"; fi\n"), 0700)
	}
	marker := filepath.Join(t.TempDir(), "invoked")
	command, err := os.ReadFile(filepath.Join(dir, "bin", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	command = bytes.Replace(command, []byte("else "), []byte("else touch '"+marker+"'; "), 1)
	if err := os.WriteFile(filepath.Join(dir, "bin", "demo"), command, 0700); err != nil {
		t.Fatal(err)
	}
	auth := connectorauth.New(t.Context(), sources, nil).WithSsoIdTokenFile(identity)
	if _, err := auth.Prepare(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if mode == "oneid" {
		pkg, err := sources.Load("demo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pkg.SetConfigured(true); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.Replace(platformToolAgent(), "name:'platform_call',arguments:{name:'fixture_lookup',arguments:{key:'project'}}", "name:'platform_connector',arguments:{connectorId:'demo',args:['literal; echo forbidden']}", 1)
	engineApproval := scenario == "approve" || scenario == "reject"
	if engineApproval {
		script = "let resolvePermission;" + strings.Replace(script, "const m=JSON.parse(line);", "const m=JSON.parse(line);if(m.id==='connector-permission'){resolvePermission(m.result);return;}", 1)
		script = strings.Replace(script, "const res=await fetch", `const permission=await new Promise(resolve=>{resolvePermission=resolve;console.log(JSON.stringify({jsonrpc:'2.0',id:'connector-permission',method:'session/request_permission',params:{sessionId:m.params.sessionId,toolCall:{toolCallId:'connector-call',title:'Engine connector permission',kind:'execute',status:'pending'},options:[{optionId:'once',kind:'allow_once',name:'Allow once'},{optionId:'reject',kind:'reject_once',name:'Reject'}]}}));});if(permission.outcome.optionId!=='once'){console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'engine denied'}}}}));send(m.id,{stopReason:'end_turn'});break;}const res=await fetch`, 1)
	}
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Connectors: []string{"demo"}, ConnectorMounts: []catalog.ConnectorMount{{ID: "demo", Dir: dir}}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	if scenario == "unmounted" {
		// Keep MCP enabled with another mount; attempting demo must still fail.
		registry.def.Connectors = []string{"other"}
		registry.def.ConnectorMounts = []catalog.ConnectorMount{{ID: "other", Dir: t.TempDir()}}
	}
	service := query.NewService(query.Dependencies{Config: config.Config{Paths: config.PathsConfig{StateDir: t.TempDir()}}, ConnectorOperations: &connectorops.Service{Auth: auth, Sources: sources, SsoIdTokenFile: identity}, Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ctx = runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: "owner"})
	handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "use CLI"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	answered := false
	var output strings.Builder
	permissionCount := 0
	wantAnswer := "literal; echo forbidden"
	if scenario == "reject" {
		wantAnswer = "engine denied"
	} else if scenario == "unmounted" {
		wantAnswer = "connector_not_mounted"
	}
	for {
		select {
		case event, ok := <-sub.Events:
			if !ok {
				t.Fatal("Run closed before completion")
			}
			switch event.Type {
			case "awaiting.ask":
				permissionCount++
				if !engineApproval || permissionCount > 1 {
					t.Fatal("Platform added an approval after the ACP engine dispatched the connector")
				}
				approval := event.Value("approvals").([]any)[0].(map[string]any)
				if !strings.Contains(fmt.Sprint(approval["description"]), "Engine connector permission") {
					t.Fatalf("unexpected approval: %+v", approval)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("connector executed before engine approval")
				}
				result, err := service.Submit(ctx, runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: event.String("awaitingId"), SubmitID: "cli", Params: []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": scenario})}})
				if err != nil || !result.Accepted {
					t.Fatalf("submit=%+v %v", result, err)
				}
			case "content.delta":
				output.WriteString(event.String("delta"))
				answered = answered || strings.Contains(event.String("delta"), wantAnswer)
			case "run.complete":
				if !answered {
					t.Fatalf("missing connector result %q: %s", wantAnswer, output.String())
				}
				if engineApproval && permissionCount != 1 {
					t.Fatal("engine approval was skipped")
				}
				_, err := os.Stat(marker)
				if scenario == "reject" || scenario == "unmounted" {
					if !os.IsNotExist(err) {
						t.Fatal("denied connector was executed")
					}
				} else if err != nil {
					t.Fatalf("connector did not execute: %v", err)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestACPEmbedsBinaryAttachmentOnlyWhenNegotiated(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(m.params.prompt)}}}}));`, 1)
			if supported {
				script = strings.Replace(script, "loadSession:true", "loadSession:true,promptCapabilities:{embeddedContext:true}", 1)
			}
			_, store, _, sessions, root := sessionFixture(t, script)
			os.MkdirAll(store.ChatDir("chat-one"), 0700)
			os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "doc.pdf"), []byte("%PDF-1.7\n\xff\x00"), 0600)
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			result, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "read", References: []runtimetypes.Reference{{Type: "file", URL: "@chat/doc.pdf"}}}, runtimetypes.QueryHooks{})
			if supported {
				if err != nil || !strings.Contains(result.Content, `"type":"resource"`) || !strings.Contains(result.Content, "application/pdf") {
					t.Fatalf("binary not embedded: %+v %v", result, err)
				}
			} else if err == nil && !strings.Contains(result.ErrorMessage, "capability_unsupported") {
				t.Fatalf("unsupported binary accepted: %+v", result)
			}
		})
	}
}

func TestACPMountedToolsRequireNegotiatedMCP(t *testing.T) {
	_, store, _, sessions, root := sessionFixture(t, sessionAgent)
	executor := &contextToolExecutor{}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Tools: []string{"fixture_lookup"}, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Tools: executor, Sessions: session.New(session.Dependencies{Chats: store, Tools: executor}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	_, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "use connector"}, runtimetypes.QueryHooks{})
	var failure *runtimetypes.RequestError
	if !errors.As(err, &failure) || failure.Code != "capability_unsupported" {
		t.Fatalf("unsupported MCP silently ignored: %v", err)
	}
}

func TestPlatformMCPRequiresTokenAndActiveRunAndClosesWithProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.json")
	pathJSON, _ := json.Marshal(path)
	script := strings.Replace(sessionAgent, "loadSession:true", "loadSession:true,mcpCapabilities:{http:true}", 1)
	script = strings.Replace(script, "case 'session/new':", "case 'session/new':require('node:fs').writeFileSync("+string(pathJSON)+",JSON.stringify(m.params.mcpServers[0]));", 1)
	_, _, _, sessions, _ := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bridge struct {
		URL     string
		Headers []struct{ Name, Value string }
	}
	if err := json.Unmarshal(data, &bridge); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	for _, auth := range []bool{false, true} {
		req, _ := http.NewRequestWithContext(ctx, "POST", bridge.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_tools","arguments":{}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth {
			for _, h := range bridge.Headers {
				req.Header.Set(h.Name, h.Value)
			}
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !auth && res.StatusCode != 401 {
			t.Fatal("missing token accepted")
		}
		if auth && !strings.Contains(string(body), "no active Platform Run") {
			t.Fatalf("inactive call accepted: %s", body)
		}
	}
	sessions.CloseChat("chat-one")
	deadline := time.Now().Add(time.Second)
	for {
		res, err := client.Get(bridge.URL)
		if err != nil {
			break
		}
		res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("MCP listener survived process close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestACPRejectsCrossChatPathAndEscapingSymlinkAttachments(t *testing.T) {
	for _, kind := range []string{"cross-chat", "path-only", "symlink", "too-large"} {
		t.Run(kind, func(t *testing.T) {
			_, store, _, sessions, root := sessionFixture(t, sessionAgent)
			os.MkdirAll(store.ChatDir("chat-one"), 0700)
			ref := runtimetypes.Reference{Type: "file", URL: "@chat/input.txt"}
			switch kind {
			case "cross-chat":
				ref.URL = "/api/resource?file=chat-other/input.txt"
			case "path-only":
				ref.URL = ""
				ref.Path = "/etc/passwd"
			case "symlink":
				os.WriteFile(filepath.Join(root, "outside.txt"), []byte("secret"), 0600)
				if err := os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(store.ChatDir("chat-one"), "input.txt")); err != nil {
					t.Fatal(err)
				}
			case "too-large":
				os.WriteFile(filepath.Join(store.ChatDir("chat-one"), "input.txt"), bytes.Repeat([]byte("x"), 11<<20), 0600)
			}
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			result, err := service.ExecuteQuery(t.Context(), runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "read", References: []runtimetypes.Reference{ref}}, runtimetypes.QueryHooks{})
			if err == nil && result.FinishReason != "error" {
				t.Fatalf("unsafe reference accepted: %+v", result)
			}
		})
	}
}

// Queue exhaustion is independent of attachment size and must not look like
// a dead Adapter. Exercise the public Query error and an explicit retry.
func TestQueryQueueTimeoutReportsBusyAndKeepsActiveSessions(t *testing.T) {
	script := strings.Replace(multiplexAgent, "loadSession:true", "loadSession:true,promptCapabilities:{image:true}", 1)
	script = strings.Replace(script, "case 'session/prompt':send", `case 'session/prompt':
fs.appendFileSync('received-prompts','1\n');
if(p.prompt[0].text==='hold') {const timer=setInterval(()=>{if(fs.existsSync('release-prompts')){clearInterval(timer);send(m.id,{stopReason:'end_turn'});}},10);break;}
send`, 1)
	_, store, host, sessions, root := sessionFixtureWithOptions(t, script, driver.Options{MaxActive: 2, QueueTimeout: 30 * time.Millisecond})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	completed := make(chan error, 2)
	for _, id := range []string{"chat-one", "chat-two"} {
		if _, _, err := store.EnsureChat(id, "project", ""); err != nil {
			t.Fatal(err)
		}
		p, err := sessions.Prepare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, err := sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("hold")}, nil)
			completed <- err
		}()
	}
	for host.Stats().Active != 2 {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := store.EnsureChat("chat-three", "project", ""); err != nil {
		t.Fatal(err)
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 843408)
	copy(data, pngData.Bytes())
	if err := os.MkdirAll(store.ChatDir("chat-three"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(store.ChatDir("chat-three"), "screenshot.png"), string(data), 0600)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	command := runtimetypes.QueryCommand{ChatID: "chat-three", AgentKey: "project", Message: "read image"}
	for _, withImage := range []bool{false, true} {
		if withImage {
			command.References = []runtimetypes.Reference{{Type: "file", URL: "@chat/screenshot.png"}}
		}
		result, err := service.ExecuteQuery(ctx, command, runtimetypes.QueryHooks{})
		if err != nil || result.ErrorPayload["code"] != "engine_busy" || result.ErrorPayload["executionState"] != "not_started" {
			t.Fatalf("image=%v: queued request misclassified: %+v %v", withImage, result, err)
		}
		if host.Stats().Active != 2 || host.Stats().Processes != 1 {
			t.Fatalf("active siblings disrupted: %+v", host.Stats())
		}
	}
	write(t, filepath.Join(root, "release-prompts"), "", 0600)
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.ExecuteQuery(ctx, command, runtimetypes.QueryHooks{})
	if err != nil || result.FinishReason != "complete" {
		t.Fatalf("explicit retry failed: %+v %v", result, err)
	}
	received, err := os.ReadFile(filepath.Join(root, "received-prompts"))
	if err != nil || strings.Count(string(received), "1\n") != 3 {
		t.Fatalf("expired prompts were replayed: %q %v", received, err)
	}
}

func TestACPArtifactLinkUsesWorkspaceScopeAcrossChunks(t *testing.T) {
	chunks := []string{"已制作独立单页海报：**[cutej-poster.html](cutej-", "poster.html)**。"}
	unchanged := " [web](https://example.com/page.html) [anchor](#design) [explicit](@workspace/cutej-poster.html) [chat](@chat/upload.png) [missing](missing.html) [escape](../outside.html) [symlink](outside.html) ` [code](cutej-poster.html) `\n```md\n[example](cutej-poster.html)\n```\n"
	chunks = append(chunks, " [nested](./docs/note.html \"Notes\") [space](<报告 1.html>)"+unchanged)
	encoded, _ := json.Marshal(chunks)
	script := strings.Replace(sessionAgent, "case 'session/prompt':", `case 'session/prompt':for(const text of `+string(encoded)+`){console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text}}}}));}`, 1)
	_, store, _, sessions, root := sessionFixture(t, script)
	write(t, filepath.Join(root, "cutej-poster.html"), "<!doctype html><title>Poster</title>", 0600)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "docs", "note.html"), "notes", 0600)
	write(t, filepath.Join(root, "报告 1.html"), "report", 0600)
	outside := filepath.Join(t.TempDir(), "outside.html")
	write(t, outside, "private", 0600)
	if err := os.Symlink(outside, filepath.Join(root, "outside.html")); err != nil {
		t.Fatal(err)
	}
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "make poster", IncludeFullText: true}, runtimetypes.QueryHooks{})
	want := "已制作独立单页海报：**[cutej-poster.html](@workspace/cutej-poster.html)**。 [nested](@workspace/docs/note.html \"Notes\") [space](<@workspace/报告 1.html>)" + unchanged
	if err != nil || result.Content != want || result.FinishReason != "complete" {
		t.Fatalf("artifact link not projected: %+v %v", result, err)
	}
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	for _, event := range detail.Events {
		if event.Type == "content.snapshot" {
			saved += event.String("text")
		}
	}
	if saved != want {
		t.Fatalf("history differs from live output: %q", saved)
	}
}
