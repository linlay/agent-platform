package acp_test

import (
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/terminal"
	"context"
	sdk "github.com/coder/acp-go-sdk"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareRechecksCredentialsRestoredOutsidePlatform(t *testing.T) {
	script := `const fs=require('node:fs'),path=require('node:path');
const send=(id,result)=>console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);switch(m.method){
case 'initialize':send(m.id,{protocolVersion:1,authMethods:[]});break;
case 'session/new':if(fs.existsSync(path.join(m.params.cwd,'local-login')))send(m.id,{sessionId:'restored',configOptions:[]});else console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));break;
case 'authenticate':case 'session/prompt':process.exit(99);
}});`
	_, _, host, sessions, root := sessionFixture(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err == nil || err.Error() != "auth_required" {
		t.Fatalf("missing login should fail: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "local-login"), []byte("restored externally"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
		t.Fatal("explicit preparation must recheck external credentials", err)
	}
	status, _ := sessions.AuthStatus("owner", "fixture/agent")
	if status.State != "unknown" || host.Stats().Processes != 1 {
		t.Fatalf("preparation must clear stale failure without claiming verified authentication: %+v %+v", status, host.Stats())
	}
}

func TestEngineAuthenticationRechecksLoginWithoutCallingModel(t *testing.T) {
	script := `const fs=require('node:fs'), path=require('node:path');const marker=path.join(process.env.ZENMIND_AGENT_PROFILE,'login-fixture');
 const send=(id,result)=>console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
 require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);switch(m.method){
 case 'initialize':send(m.id,{protocolVersion:1,authMethods:[{id:'browser-login',name:'Browser'},{id:'api-key',name:'Key',_meta:{'api-key':{}}}]});break;
 case 'authenticate':fs.writeFileSync(marker,'yes');send(m.id,{});break;
 case 'session/new':if(fs.existsSync(marker))send(m.id,{sessionId:'verified',configOptions:[]});else console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));break;
 case 'session/prompt':process.exit(99);
 }});`
	_, _, host, sessions, _ := sessionFixture(t, script, func(r *dto.EngineResource) {
		r.Authentication = &dto.EngineAuthentication{TerminalMethods: []dto.TerminalAuthLaunch{}, AgentMethods: []string{"browser-login"}}
	})
	sessions.EnableAuthentication(terminal.NewManager())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sessions.Prepare(ctx, "chat-one"); err == nil {
		t.Fatal("unlogged session prepared")
	}
	before, err := sessions.AuthStatus("owner", "fixture/agent")
	if err != nil || before.State != "required" || len(before.Methods) != 2 || before.Methods[1].Available {
		t.Fatalf("status: %+v %v", before, err)
	}
	// Reauthentication must recheck a fresh process without losing the Chat.
	for attempt := 0; attempt < 2; attempt++ {
		started, err := sessions.StartAuthentication("owner", "fixture/agent", "browser-login")
		if err != nil {
			t.Fatal(err)
		}
		if started.AuthOperationID == nil || started.State != "authenticating" {
			t.Fatalf("not started: %+v", started)
		}
		var state string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			status, err := sessions.AuthStatus("owner", "fixture/agent")
			if err != nil {
				t.Fatal(err)
			}
			state = status.State
			if state != "authenticating" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if state != "authenticated" {
			t.Fatal("not reverified", state)
		}
		status, _ := sessions.AuthStatus("owner", "fixture/agent")
		if status.AuthOperationID != nil {
			t.Fatal("completed authentication retained operation")
		}
		if _, err := sessions.Prepare(ctx, "chat-one"); err != nil {
			t.Fatal("profile not reused", err)
		}
		if host.Stats().Processes != 1 {
			t.Fatal("authentication leaked a process", host.Stats())
		}
	}
}

func TestEngineAuthenticationRequiresDesktopAndDoesNotTrustSuccessfulRPC(t *testing.T) {
	script := strings.Replace(sessionAgent, "agentCapabilities:{loadSession:true,sessionCapabilities:{resume:{}}}", "authMethods:[{id:'browser-login',name:'Browser'}]", 1)
	script = strings.Replace(script, "case 'session/new':send(m.id,{sessionId:'session-'+(++count),configOptions:options()});break;", `case 'authenticate':send(m.id,{});break;case 'session/new':console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));break;`, 1)
	_, _, _, sessions, _ := sessionFixture(t, script, func(r *dto.EngineResource) {
		r.Authentication = &dto.EngineAuthentication{TerminalMethods: []dto.TerminalAuthLaunch{}, AgentMethods: []string{"browser-login"}}
	})
	_, _ = sessions.Prepare(context.Background(), "chat-one")
	if _, err := sessions.StartAuthentication("owner", "fixture/agent", "browser-login"); err == nil || err.Error() != "desktop_required" {
		t.Fatal(err)
	}
	sessions.EnableAuthentication(terminal.NewManager())
	started, err := sessions.StartAuthentication("owner", "fixture/agent", "browser-login")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.CancelAuthentication("other", *started.AuthOperationID); err == nil {
		t.Fatal("cross-owner cancel allowed")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		state, _ := sessions.AuthStatus("owner", "fixture/agent")
		if state.State != "authenticating" {
			if state.State == "authenticated" {
				t.Fatal("successful RPC was treated as login")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auth did not finish")
}

func TestTerminalAuthenticationUsesTrustedLaunchAndCancellation(t *testing.T) {
	script := `const fs=require('node:fs'),path=require('node:path'); const marker=path.join(process.env.ZENMIND_AGENT_PROFILE,'terminal-login');
 if(process.argv.includes('--login')) { console.log('Login fixture'); process.stdin.once('data',()=>{fs.writeFileSync(marker,'yes');process.exit(0)}); }
 else require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line); const send=result=>console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result}));
 if(m.method==='initialize'){if(m.params.clientCapabilities?.auth?.terminal!==true){send({protocolVersion:1,authMethods:[]});return;}send({protocolVersion:1,authMethods:[{id:'login',name:'Login',type:'terminal',args:['--login'],_meta:{'terminal-auth':{command:'/untrusted/command'}}}]});}
 else if(m.method==='authenticate')process.exit(99);
 else if(m.method==='session/new') {if(fs.existsSync(marker))send({sessionId:'ready'});else console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}));}
 });`
	_, _, host, sessions, _ := sessionFixture(t, script, func(r *dto.EngineResource) {
		r.Authentication = &dto.EngineAuthentication{TerminalMethods: []dto.TerminalAuthLaunch{{MethodID: "login", Args: []string{"--login"}}}}
	})
	manager := terminal.NewManager()
	sessions.EnableAuthentication(manager)
	_, _ = sessions.Prepare(context.Background(), "chat-one")
	for _, cancel := range []bool{true, false} {
		started, err := sessions.StartAuthentication("owner", "fixture/agent", "login")
		if err != nil {
			t.Fatal(err)
		}
		var opened terminal.OpenResult
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			opened, err = sessions.AuthTerminal("owner", "fixture/agent", *started.AuthOperationID)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.AuthTerminal("other", "fixture/agent", *started.AuthOperationID); err == nil {
			t.Fatal("cross-owner attach allowed")
		}
		if cancel {
			if err := sessions.CancelAuthentication("owner", *started.AuthOperationID); err != nil {
				t.Fatal(err)
			}
			if !opened.Session.Finished() || host.Stats().Processes != 0 {
				t.Fatal("login cancellation leaked processes")
			}
		} else {
			if err := manager.Input("owner", opened.Session.ID(), "confirm\n"); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				status, _ := sessions.AuthStatus("owner", "fixture/agent")
				if status.State == "authenticated" {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("terminal login not verified")
		}
	}
}

func TestAuthenticationFailureDuringPromptRechecksBeforeExplicitRetry(t *testing.T) {
	script := strings.Replace(sessionAgent, "agentCapabilities:{loadSession:true,sessionCapabilities:{resume:{}}}", "authMethods:[{id:'browser-login',name:'Browser'}]", 1)
	script = strings.Replace(script, "send(m.id,{stopReason:'end_turn',sessionId:m.params.sessionId,model})", "console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,error:{code:-32000,message:'Authentication required'}}))", 1)
	_, _, host, sessions, _ := sessionFixture(t, script, func(r *dto.EngineResource) {
		r.Authentication = &dto.EngineAuthentication{TerminalMethods: []dto.TerminalAuthLaunch{}, AgentMethods: []string{"browser-login"}}
	})
	p, err := sessions.Prepare(context.Background(), "chat-one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sessions.Prompt(context.Background(), p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	status, _ := sessions.AuthStatus("owner", "fixture/agent")
	if status.State != "required" {
		t.Fatal(status.State)
	}
	retry, err := sessions.Prepare(context.Background(), "chat-one")
	if err != nil || retry.PreparationID == p.PreparationID {
		t.Fatal("explicit retry must prepare a fresh process", err)
	}
	status, _ = sessions.AuthStatus("owner", "fixture/agent")
	if status.State != "unknown" {
		t.Fatal("preparation must not claim authentication", status.State)
	}
	if _, err = sessions.Prompt(context.Background(), retry.ChatID, retry.PreparationID, retry.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("explicit retry")}, nil); err == nil {
		t.Fatal("still-invalid credentials must fail again")
	}
	status, _ = sessions.AuthStatus("owner", "fixture/agent")
	if status.State != "required" {
		t.Fatal("failed explicit retry must require authentication", status.State)
	}
	if host.Stats().Processes != 0 {
		t.Fatal("unlogged process retained")
	}
}

func TestPreparedSessionDoesNotClaimAuthenticationBeforeACompletedPrompt(t *testing.T) {
	for _, test := range []struct{ stop, state string }{{"end_turn", "authenticated"}, {"cancelled", "unknown"}} {
		t.Run(test.stop, func(t *testing.T) {
			script := strings.Replace(sessionAgent, "stopReason:'end_turn'", "stopReason:'"+test.stop+"'", 1)
			_, _, _, sessions, _ := sessionFixture(t, script, func(r *dto.EngineResource) {
				r.Authentication = &dto.EngineAuthentication{TerminalMethods: []dto.TerminalAuthLaunch{}, AgentMethods: []string{"browser-login"}}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := sessions.Prepare(ctx, "chat-one")
			if err != nil {
				t.Fatal(err)
			}
			status, err := sessions.AuthStatus("owner", "fixture/agent")
			if err != nil || status.State != "unknown" {
				t.Fatalf("session/new is not an authentication check: %+v %v", status, err)
			}
			if _, err = sessions.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, []sdk.ContentBlock{sdk.TextBlock("one")}, nil); err != nil {
				t.Fatal(err)
			}
			status, _ = sessions.AuthStatus("owner", "fixture/agent")
			if status.State != test.state {
				t.Fatalf("prompt %s: got %+v, want %s", test.stop, status, test.state)
			}
		})
	}
}
