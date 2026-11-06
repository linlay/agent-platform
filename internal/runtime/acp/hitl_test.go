package acp_test

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/runtime/query"
	"agent-platform/internal/runtime/runstate"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExternalPermissionUsesExistingSubmit(t *testing.T) {
	script := interactionAgent("session/request_permission", `{toolCall:{toolCallId:'tool-1',title:'Edit files',locations:[{path:'/workspace/approved.txt'},{path:'/workspace/second.txt'}]},options:[{optionId:'once',name:'Allow once',kind:'allow_once'},{optionId:'always',name:'Always',kind:'allow_always'}]}`)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	ctx = runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: "owner"})
	handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
	if ask.String("mode") != "approval" {
		t.Fatalf("ask=%+v", ask)
	}
	approvals, _ := ask.Value("approvals").([]any)
	if len(approvals) != 1 {
		t.Fatalf("approvals=%+v", ask)
	}
	approval := approvals[0].(map[string]any)
	description, _ := approval["description"].(string)
	if !strings.Contains(description, "/workspace/approved.txt") || !strings.Contains(description, "/workspace/second.txt") {
		t.Fatalf("missing targets: %+v", ask)
	}
	for _, field := range []string{"engineId", "options", "source"} {
		if ask.Value(field) != nil {
			t.Fatalf("ACP field escaped: %s", field)
		}
	}
	command := runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "decision-1", Params: []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": "approve"})}}
	for _, bad := range []map[string]any{{"id": approval["id"], "decision": "approve_rule_run"}, {"id": "forged", "decision": "approve"}} {
		invalid := command
		invalid.Params = []json.RawMessage{encoded(t, bad)}
		result, err := service.Submit(ctx, invalid)
		if err == nil && result.Accepted {
			t.Fatalf("unsafe submit accepted: %+v", bad)
		}
	}
	wrong := command
	wrong.ChatID = "another-chat"
	if result, err := service.Submit(ctx, wrong); err == nil && result.Accepted {
		t.Fatal("cross-chat submit accepted")
	}
	other := runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: "other"})
	if result, err := service.Submit(other, command); err == nil && result.Accepted {
		t.Fatal("cross-owner submit accepted")
	}
	result, err := service.Submit(ctx, command)
	if err != nil || !result.Accepted {
		t.Fatalf("submit=%+v %v", result, err)
	}
	if result, err := service.Submit(ctx, command); err != nil || !result.Accepted {
		t.Fatalf("duplicate=%+v %v", result, err)
	}
	conflict := command
	conflict.Params = []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": "reject"})}
	if result, err := service.Submit(ctx, conflict); err == nil && result.Accepted {
		t.Fatal("same submitId with changed body accepted")
	}

	answer := awaitEvent(t, ctx, sub.Events, "content.delta")
	if !strings.Contains(answer.String("delta"), `"optionId":"once"`) {
		t.Fatalf("wrong RPC result: %+v", answer)
	}
	awaitEvent(t, ctx, sub.Events, "run.complete")
	detail, err := store.LoadChat("chat-one")
	if err != nil {
		t.Fatal(err)
	}
	var decision map[string]any
	for _, event := range detail.Events {
		if event.Type == "awaiting.answer" {
			items, _ := event.Value("approvals").([]any)
			if len(items) == 1 {
				decision, _ = items[0].(map[string]any)
			}
		}
	}
	if decision["decision"] != "approve" || decision["id"] != approval["id"] {
		t.Fatalf("history lost neutral decision: %+v", decision)
	}
	restarted := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager()})
	if reply, err := restarted.Submit(ctx, command); err == nil && reply.Accepted {
		t.Fatal("external receipt accepted after losing connection generation")
	}

}

func awaitEvent(t *testing.T, ctx context.Context, events <-chan stream.EventData, kind string) stream.EventData {
	t.Helper()
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("stream closed before %s", kind)
			}
			if e.Type == "run.error" {
				t.Fatalf("run failed before %s: %+v", kind, e)
			}
			if e.Type == kind {
				return e
			}
		case <-ctx.Done():
			t.Fatalf("timed out awaiting %s", kind)
		}
	}
}

func interactionAgent(method, params string) string {
	script := strings.Replace(sessionAgent, "let count=0", "let promptID;let count=0", 1)
	script = strings.Replace(script, "switch(m.method){", `if(m.id==='interaction' && !m.method){
 console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:'session-1',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(m.result)}}}}));
 send(promptID,{stopReason:'end_turn'});return;}switch(m.method){`, 1)
	script = strings.Replace(script, "case 'session/prompt':send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model});break;", `case 'session/prompt':promptID=m.id;{const params=`+params+`;params.sessionId=m.params.sessionId;console.log(JSON.stringify({jsonrpc:'2.0',id:'interaction',method:'`+method+`',params}));}break;`, 1)
	return script
}

func TestElicitationPreservesFieldAndEnumIDs(t *testing.T) {
	script := interactionAgent("elicitation/create", `{mode:'form',toolCallId:'ask',message:'Choose',requestedSchema:{type:'object',properties:{color:{type:'string',_meta:{codex:{isOther:true,isSecret:false}},oneOf:[{const:'BLUE',title:'蓝色',description:'Blue option help'},{const:'RED',title:'红色'}]},note:{type:'string',title:'备注'}},required:['color','note']}}`)
	_, store, _, sessions, root := sessionFixture(t, script)
	registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
	service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
	if ask.String("mode") != "question" {
		t.Fatalf("ask=%+v", ask)
	}
	questions, _ := ask.Value("questions").([]any)
	first, _ := questions[0].(map[string]any)
	if !strings.Contains(first["question"].(string), "Choose") {
		t.Fatalf("lost upstream question: %+v", first)
	}
	options, _ := first["options"].([]any)
	if len(options) == 0 || !strings.Contains(options[0].(map[string]any)["label"].(string), "Blue option help") {
		t.Fatal("option description lost")
	}
	if first["required"] != nil || options[0].(map[string]any)["id"] != nil || options[0].(map[string]any)["value"] != "BLUE" {
		t.Fatalf("ACP question did not use the existing AGW option value: %+v", first)
	}
	command := runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "answer", Params: []json.RawMessage{json.RawMessage(`{"id":"note","answer":"你好"}`), json.RawMessage(`{"id":"color","answer":"BLUE"}`)}}
	bad := command
	bad.Params = []json.RawMessage{json.RawMessage(`{"id":"color","answer":"blue"}`), json.RawMessage(`{"id":"note","answer":"hi"}`)}
	if result, err := service.Submit(ctx, bad); err == nil && result.Accepted {
		t.Fatal("invalid enum accepted")
	}
	result, err := service.Submit(ctx, command)
	if err != nil || !result.Accepted {
		t.Fatalf("submit=%+v %v", result, err)
	}
	answer := awaitEvent(t, ctx, sub.Events, "content.delta")
	if !strings.Contains(answer.String("delta"), `"action":"accept"`) || !strings.Contains(answer.String("delta"), `"color":"BLUE"`) {
		t.Fatalf("response=%+v", answer)
	}
	awaitEvent(t, ctx, sub.Events, "run.complete")
}

func TestExternalWaitEndsWithRunAndRejectsLateSubmit(t *testing.T) {
	for _, ending := range []string{"cancel", "crash", "kill", "eof", "early_end"} {
		t.Run(ending, func(t *testing.T) {
			script := interactionAgent("session/request_permission", `{toolCall:{title:'Confirm'},options:[{optionId:'once',name:'Once',kind:'allow_once'}]}`)
			if ending != "cancel" {
				action := map[string]string{"kill": "process.kill(process.pid, 'SIGKILL')", "crash": "process.exit(23)", "eof": "require('node:fs').closeSync(1)", "early_end": "send(promptID,{stopReason:'end_turn'})"}[ending]
				script = strings.Replace(script, "params}));}break;", "params}));setTimeout(()=>"+action+",100);}break;", 1)
			}
			// Real adapters announce the tool before asking for permission.
			script = strings.Replace(script, "case 'session/prompt':", `case 'session/prompt':console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'tool_call',toolCallId:'pending-write',title:'Write',kind:'edit',status:'pending',rawInput:{path:'pending.txt'}}}}));`, 1)
			_, store, host, sessions, root := sessionFixture(t, script)
			runs := runstate.NewManager()
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runs, Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"})
			if err != nil {
				t.Fatal(err)
			}
			sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
			if ending == "cancel" {
				sub.Close()
				sub, err = service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, ask.Seq)
				if err != nil {
					t.Fatal(err)
				}
				defer sub.Close()
				if _, err = service.Interrupt(ctx, runtimetypes.InterruptCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, AgentKey: "project"}}); err != nil {
					t.Fatal(err)
				}
			}
			terminal := map[string]int{}
		drain:
			for {
				select {
				case event, ok := <-sub.Events:
					if !ok {
						break drain
					}
					if event.Type == "run.error" || event.Type == "run.cancel" || event.Type == "run.complete" {
						terminal[event.Type]++
					}
				case <-ctx.Done():
					t.Fatal("run did not finish")
				}
			}
			expected := "run.error"
			if ending == "cancel" {
				expected = "run.cancel"
			}
			if len(terminal) != 1 || terminal[expected] != 1 {
				t.Fatalf("wrong terminal: %v", terminal)
			}
			for len(runs.ActiveAwaitings(handle.RunID)) > 0 || host.Stats().Processes != 0 {
				select {
				case <-ctx.Done():
					t.Fatalf("leaked waiting/process state: %+v %+v", runs.ActiveAwaitings(handle.RunID), host.Stats())
				case <-time.After(10 * time.Millisecond):
				}
			}
			detail, historyErr := store.LoadChat("chat-one")
			if historyErr != nil {
				t.Fatalf("terminated tool history cannot be loaded: %v", historyErr)
			}
			results := 0
			for _, event := range detail.Events {
				if event.Type == "tool.result" && event.String("toolId") == "pending-write" {
					if !strings.Contains(event.String("result"), "External tool did not complete successfully") {
						t.Fatalf("unfinished tool reported success: %+v", event)
					}
					results++
				}
			}
			if results != 1 {
				t.Fatalf("pending tool must have one terminal result, got %d", results)
			}
			if ending != "cancel" {
				binding, err := store.LoadEngineSession("chat-one")
				if err != nil || binding.ContextState != "submitted" {
					t.Fatalf("uncertain prompt lost: %+v %v", binding, err)
				}
			}
			command := runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "late", Param: map[string]any{"outcome": "selected", "optionId": "once"}}
			if result, err := service.Submit(ctx, command); err == nil && result.Accepted {
				t.Fatal("late submit accepted")
			}
		})
	}
}

func TestUnsupportedElicitationFailsWithoutPretendingToAsk(t *testing.T) {
	for name, params := range map[string]string{
		"optional":            `{mode:"form",message:"Optional",requestedSchema:{type:"object",properties:{note:{type:"string"}}}}`,
		"secret":              `{mode:'form',message:'Password',requestedSchema:{type:'object',properties:{password:{type:'string',_meta:{codex:{isSecret:true}}}}}}`,
		"other-vendor-secret": `{mode:'form',message:'Password',requestedSchema:{type:'object',properties:{password:{type:'string',_meta:{other:{isSecret:true}}}}}}`,
		"unknown-hint":        `{mode:'form',message:'Password',requestedSchema:{type:'object',properties:{password:{type:'string',_meta:{other:{password:true}}}}}}`,
		"number":              `{mode:'form',message:'Count',requestedSchema:{type:'object',properties:{count:{type:'number'}}}}`,
		"url":                 `{mode:'url',message:'Login',url:'https://example.invalid/'}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, store, _, sessions, root := sessionFixture(t, interactionAgent("elicitation/create", params))
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			// A regression that opens HITL must fail promptly, not wait for user input.
			runID := "unsupported-" + name
			stop := context.AfterFunc(ctx, func() {
				service.Interrupt(context.Background(), runtimetypes.InterruptCommand{RunRef: runtimetypes.RunRef{RunID: runID, AgentKey: "project"}})
			})
			defer stop()
			result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{RunID: runID, ChatID: "chat-one", AgentKey: "project", Message: "work"}, runtimetypes.QueryHooks{})
			if err != nil || result.FinishReason != "error" || result.ErrorPayload["code"] != "elicitation_unsupported" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExternalInteractionCanBeDismissedWithoutGrantingPermission(t *testing.T) {
	for _, method := range []string{"session/request_permission", "elicitation/create"} {
		t.Run(method, func(t *testing.T) {
			params := `{toolCall:{title:'One time'},options:[{optionId:' Exact-Allow ',name:'Allow',kind:'allow_once'}]}`
			if method == "elicitation/create" {
				params = `{mode:'form',message:'Name',requestedSchema:{type:'object',properties:{name:{type:'string'}},required:['name']}}`
			}
			_, store, _, sessions, root := sessionFixture(t, interactionAgent(method, params))
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"})
			if err != nil {
				t.Fatal(err)
			}
			sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
			cmd := runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{ChatID: "chat-one", RunID: handle.RunID, AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "dismiss"}
			cmd.Params = []json.RawMessage{}
			expected := `"outcome":"cancelled"`
			if method == "elicitation/create" {
				expected = `"action":"cancel"`
			}

			if result, err := service.Submit(ctx, cmd); err != nil || !result.Accepted {
				t.Fatalf("dismiss=%+v %v", result, err)
			}
			answer := awaitEvent(t, ctx, sub.Events, "content.delta")
			if !strings.Contains(answer.String("delta"), expected) {
				t.Fatalf("wrong cancellation reply: %+v", answer)
			}
			awaitEvent(t, ctx, sub.Events, "run.complete")
		})
	}
}

func TestApprovalRejectMapsOnlyToOneTimeUpstreamChoice(t *testing.T) {
	for _, rejectOption := range []string{"", ",{optionId:' Exact-Reject ',name:'No',kind:'reject_once'}"} {
		t.Run(rejectOption, func(t *testing.T) {
			params := `{toolCall:{title:'Write file'},options:[{optionId:' Exact-Allow ',name:'Yes',kind:'allow_once'}` + rejectOption + `,{optionId:'forever',name:'Always',kind:'allow_always'}]}`
			_, store, _, sessions, root := sessionFixture(t, interactionAgent("session/request_permission", params))
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"})
			if err != nil {
				t.Fatal(err)
			}
			sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ask := awaitEvent(t, ctx, sub.Events, "awaiting.ask")
			approval := ask.Value("approvals").([]any)[0].(map[string]any)
			reply, err := service.Submit(ctx, runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "chat-one", AgentKey: "project"}, AwaitingID: ask.String("awaitingId"), SubmitID: "reject", Params: []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": "reject"})}})
			if err != nil || !reply.Accepted {
				t.Fatalf("reject=%+v %v", reply, err)
			}
			answer := awaitEvent(t, ctx, sub.Events, "content.delta")
			expected := `"outcome":"cancelled"`
			if rejectOption != "" {
				expected = `"optionId":" Exact-Reject "`
			}
			if !strings.Contains(answer.String("delta"), expected) {
				t.Fatalf("wrong rejection: %+v", answer)
			}
			awaitEvent(t, ctx, sub.Events, "run.complete")
		})
	}
}

func TestUnrepresentablePermissionFailsWithoutAwaiting(t *testing.T) {
	for name, options := range map[string]string{
		"persistent-only": `[{optionId:'forever',name:'Always',kind:'allow_always'}]`,
		"ambiguous-once":  `[{optionId:'one',name:'One',kind:'allow_once'},{optionId:'two',name:'Two',kind:'allow_once'}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, store, _, sessions, root := sessionFixture(t, interactionAgent("session/request_permission", `{toolCall:{title:'Write file'},options:`+options+`}`))
			registry := queryCatalog{def: catalog.AgentDefinition{Key: "project", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, Workspace: catalog.AgentWorkspaceConfig{Root: root}}}
			service := query.NewService(query.Dependencies{Chats: store, Registry: registry, Runs: runstate.NewManager(), Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "chat-one", AgentKey: "project", Message: "work"}, runtimetypes.QueryHooks{})
			if err != nil || result.ErrorPayload["code"] != "capability_unsupported" {
				t.Fatalf("unsupported grant=%+v %v", result, err)
			}
			detail, err := store.LoadChat("chat-one")
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range detail.Events {
				if event.Type == "awaiting.ask" {
					t.Fatal("published an ambiguous or persistent grant")
				}
			}
		})
	}
}
