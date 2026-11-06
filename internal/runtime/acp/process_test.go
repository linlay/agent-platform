package acp_test

import (
	"agent-platform/internal/processgroup"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/connector"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/httpclient"
	driver "agent-platform/internal/runtime/acp"
	sdk "github.com/coder/acp-go-sdk"
)

func write(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func encoded(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func fixture(t *testing.T, script string, configure ...func(*dto.EngineResource)) (*agentengine.Service, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("trusted filesystem fixture requires macOS; Windows native acceptance remains T10")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	delivery := filepath.Join(root, "desktop", "data", "agent-engines")
	os.MkdirAll(delivery, 0700)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(node)
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "desktop", "bin", "node")
	write(t, managed, string(bin), 0700)
	probe := exec.Command(node, "-p", "process.versions.node")
	probe.Env = []string{}
	version, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(root, "bootstrap.json")
	write(t, boot, string(encoded(t, dto.ManagementBootstrap{SchemaVersion: 1, HostID: "host", LaunchNonce: "launch", ManagementSecret: strings.Repeat("a", 64), DeliveryRoot: delivery})), 0600)
	pkg := filepath.Join(delivery, "install", "package")
	m := dto.EnginePluginManifest{PluginAPIVersion: 2, ID: "fixture", Name: "Fixture", Description: "test", Version: "1.0.0", Platform: dto.PlatformTarget{Os: runtime.GOOS, Arch: runtime.GOARCH}}
	m.Desktop.BundleTopLevelDir = m.ID
	m.Requires.PlatformAgentEngineAPI = 2
	m.Requires.WebclientAgentEngineAPI = 1
	m.Resources.AgentEngines = []dto.EngineResource{{ID: "agent", Name: "Agent", Protocol: "acp", Transport: "stdio", Launch: dto.EngineLaunch{Runtime: "node", Entry: "agent.cjs", Args: []string{}}, Adapter: &dto.AdapterVersion{Package: "@example/orbit-adapter", Version: "1.0.0", ACPSDKVersion: "2.0.2"}, Dependencies: []dto.EngineDependency{{ID: "node", Version: strings.TrimSpace(string(version)), Delivery: "host-managed"}}}}
	for _, apply := range configure {
		apply(&m.Resources.AgentEngines[0])
	}
	if m.Resources.AgentEngines[0].Launch.Runtime == "executable" {
		os.Remove(managed)
		m.Resources.AgentEngines[0].Adapter = nil
		write(t, filepath.Join(pkg, m.Resources.AgentEngines[0].Launch.Entry), script, 0700)
	} else {
		write(t, filepath.Join(pkg, "agent.cjs"), script, 0600)
	}
	write(t, filepath.Join(pkg, "manifest.json"), string(encoded(t, m)), 0600)
	if m.Resources.AgentEngines[0].Launch.Runtime == "node" {
		write(t, filepath.Join(pkg, "node_modules/@example/orbit-adapter/package.json"), `{"name":"@example/orbit-adapter","version":"1.0.0"}`, 0600)
		write(t, filepath.Join(pkg, "node_modules/@agentclientprotocol/sdk/package.json"), `{"name":"@agentclientprotocol/sdk","version":"2.0.2"}`, 0600)
	}
	digest, err := connector.RuntimeFingerprint(pkg)
	if err != nil {
		t.Fatal(err)
	}
	record := dto.TrustedDeliveryRecord{SchemaVersion: 1, InstallationRef: "install", OwnerHostID: "host", PluginID: m.ID, PluginVersion: m.Version, Platform: m.Platform, ArtifactDigest: "sha256:" + digest, Source: dto.DeliverySource{Kind: "local-import", AuthorizationRef: "local-import:install", ArchiveDigest: "sha256:" + strings.Repeat("b", 64)}}
	write(t, filepath.Join(delivery, "install", "record.json"), string(encoded(t, record)), 0600)
	registry, err := agentengine.New(filepath.Join(root, "runtime"), boot, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.Close() })
	_, err = registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{{EngineID: "fixture/agent", PluginID: m.ID, PluginVersion: m.Version, InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	return registry, root
}

const echoAgent = `const readline=require('node:readline');
readline.createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);if(m.method)console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{method:m.method,params:m.params,cwd:process.cwd()}}))});`

func TestPlatformKillCleansOrphansBeforeCollectingSnapshot(t *testing.T) {
	if root := os.Getenv("ACP_KILL_FIXTURE_ROOT"); root != "" {
		registry, err := agentengine.New(filepath.Join(root, "runtime"), filepath.Join(root, "child-bootstrap.json"), true, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "child-snapshot.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Apply("snapshot", data); err != nil {
			t.Fatal(err)
		}
		host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := host.Start(context.Background(), "orphan-probe", "fixture/agent", root, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err = p.Request(context.Background(), "pid", nil)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, "pids.json"), string(data), 0600)
		select {}
	}
	registry, root := fixture(t, `const child=require('node:child_process').spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore',detached:true});require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{leader:process.pid,child:child.pid}}))});`)
	delivery := filepath.Join(root, "desktop/data/agent-engines")
	data, err := os.ReadFile(filepath.Join(delivery, "install/record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record dto.TrustedDeliveryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "child-bootstrap.json"), string(encoded(t, dto.ManagementBootstrap{SchemaVersion: 1, HostID: "host", LaunchNonce: "child", ManagementSecret: strings.Repeat("a", 64), DeliveryRoot: delivery})), 0600)
	write(t, filepath.Join(root, "child-snapshot.json"), string(encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{{EngineID: "fixture/agent", PluginID: "fixture", PluginVersion: "1.0.0", InstallationRef: "install", ArtifactDigest: record.ArtifactDigest, Enabled: true}}})), 0600)
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(os.Args[0], "-test.run=^TestPlatformKillCleansOrphansBeforeCollectingSnapshot$")
	owner.Env = append(os.Environ(), "ACP_KILL_FIXTURE_ROOT="+root)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Process.Kill(); owner.Wait() })
	var pids struct{ Leader, Child int }
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(root, "pids.json")); err == nil && json.Unmarshal(data, &pids) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pids.Leader <= 0 || pids.Child <= 0 {
		t.Fatal("owner did not prepare process tree")
	}
	// Only this fixture's freshly created process group is eligible for cleanup.
	t.Cleanup(func() {
		exec.Command("/bin/kill", "-KILL", "--", fmt.Sprint(-pids.Leader)).Run()
		exec.Command("/bin/kill", "-KILL", "--", fmt.Sprint(-pids.Child)).Run()
	})
	snapshot := filepath.Join(root, "runtime/ru-agent-engines/fixture/agent", strings.TrimPrefix(record.ArtifactDigest, "sha256:"), "agent.cjs")
	observer, err := agentengine.New(filepath.Join(root, "runtime"), "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatal("live owner's snapshot collected", err)
	}
	data, err = exec.Command("/bin/ps", "-p", fmt.Sprint(pids.Leader), "-o", "ppid=").Output()
	if err != nil {
		t.Fatal(err)
	}
	guardian, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || guardian <= 0 || guardian == owner.Process.Pid {
		t.Fatal("missing independent guardian")
	}
	// Delay the cleanup owner deliberately: a new host must still respect its
	// inherited lease during the window after Platform death and before cleanup.
	if err := exec.Command("/bin/kill", "-STOP", fmt.Sprint(guardian)).Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("/bin/kill", "-CONT", fmt.Sprint(guardian)).Run() })
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	alive := func(pid int) bool { return exec.Command("/bin/kill", "-0", fmt.Sprint(pid)).Run() == nil }
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); err != nil && (alive(pids.Leader) || alive(pids.Child)) {
		t.Fatal("orphan snapshot collected before tree cleanup", err)
	}
	if err := exec.Command("/bin/kill", "-CONT", fmt.Sprint(guardian)).Run(); err != nil && alive(guardian) {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (alive(guardian) || alive(pids.Leader) || alive(pids.Child)) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(guardian) || alive(pids.Leader) || alive(pids.Child) {
		t.Fatal("Platform SIGKILL left guardian/adapter/child alive")
	}
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatal("orphan cleanup retained snapshot", err)
	}
}

func TestVerifiedSnapshotStartsPersistentStdio(t *testing.T) {
	registry, root := fixture(t, echoAgent)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := host.Start(ctx, "chat-one", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"initialize", "session/new"} {
		value, err := process.Request(ctx, method, map[string]any{"keep": "原样"})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Method, Cwd string
			Params      map[string]string
		}
		if err = json.Unmarshal(value, &result); err != nil {
			t.Fatal(err)
		}
		canonical, _ := filepath.EvalSymlinks(root)
		if result.Method != method || result.Cwd != canonical || result.Params["keep"] != "原样" {
			t.Fatalf("unexpected result: %s", value)
		}
	}
	if process.Generation() == "" {
		t.Fatal("missing connection generation")
	}
	if err = process.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("process still active after Close")
	}
}

func TestUnconfirmedGuardianExitRetainsSnapshotLease(t *testing.T) {
	registry, root := fixture(t, `setInterval(()=>{},1000);require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{pid:process.pid,guardian:process.ppid}}))});`)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	p, err := host.Start(context.Background(), "guardian-failure", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := p.Request(ctx, "pid", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids struct{ Pid, Guardian int }
	if err := json.Unmarshal(data, &ids); err != nil || ids.Pid <= 0 || ids.Guardian <= 0 {
		t.Fatal("invalid owned process IDs")
	}
	defer exec.Command("/bin/kill", "-KILL", "--", fmt.Sprint(-ids.Pid)).Run()
	if err := exec.Command("/bin/kill", "-ABRT", fmt.Sprint(ids.Guardian)).Run(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("guardian failure did not finish supervision")
	}
	if err := p.Close(); err == nil {
		t.Fatal("unconfirmed cleanup reported success")
	}
	if _, err := registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{}})); err != nil {
		t.Fatal(err)
	}
	// The failed process remains in host capacity; its lock must survive GC too.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	if err := registry.Collect(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "runtime/ru-agent-engines/fixture/agent/*/agent.cjs"))
	if err != nil || len(files) != 1 || host.Stats().Processes != 1 {
		t.Fatal("unconfirmed tree lost lease or capacity")
	}
}

func TestReverseRequestsKeepRawIDsAndCorrelationWhileOutboundIsPending(t *testing.T) {
	script := `const r=require('node:readline').createInterface({input:process.stdin});let pending;
 r.on('line',line=>{const m=JSON.parse(line);if(m.method==='session/prompt'){
 pending=m.id;console.log(JSON.stringify({jsonrpc:'2.0',id:pending,method:'elicitation/create',params:{sessionId:'s1',toolCallId:'t1',extra:{opaque:true}}}));
 }else if(m.id===pending && m.result){console.log(JSON.stringify({jsonrpc:'2.0',id:pending,result:m.result}));}});`
	registry, root := fixture(t, script)
	host, _ := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var received driver.Incoming
	p, err := host.Start(ctx, "chat", "fixture/agent", root, func(_ context.Context, in driver.Incoming) (any, *sdk.RequestError) {
		received = in
		return map[string]string{"action": "cancel"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Request(ctx, "session/prompt", map[string]string{"sessionId": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"action":"cancel"}` || len(received.ID) == 0 || received.Generation != p.Generation() || !strings.Contains(string(received.Params), `"toolCallId":"t1"`) || !strings.Contains(string(received.Params), `"opaque":true`) {
		t.Fatalf("lost reverse request identity: %+v %s", received, result)
	}
}

func TestTimeoutAndLeaderCrashKillDescendantsAndReleaseSnapshot(t *testing.T) {
	for _, ending := range []string{"timeout", "crash", "kill"} {
		t.Run(ending, func(t *testing.T) {
			// A long-running detached worker must remain owned after Adapter death.
			script := `const {spawn}=require('node:child_process');const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore',detached:true});
 const rl=require('node:readline').createInterface({input:process.stdin});rl.on('line',line=>{const m=JSON.parse(line);if(m.method==='crash')process.exit(23);if(m.method==='kill')process.kill(process.pid,'SIGKILL');if(m.method==='pid')setTimeout(()=>console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{pid:child.pid}})),100);});`
			registry, root := fixture(t, script)
			host, _ := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
			defer host.Close()
			p, err := host.Start(context.Background(), "chat", "fixture/agent", root, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			data, err := p.Request(ctx, "pid", nil)
			if err != nil {
				t.Fatal(err)
			}
			var result struct{ Pid int }
			json.Unmarshal(data, &result)
			_, err = registry.Apply("snapshot", encoded(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{}}))
			if err != nil {
				t.Fatal(err)
			}
			short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer stop()
			method := "hang"
			if ending != "timeout" {
				method = ending
			}
			if _, err = p.Request(short, method, nil); err == nil {
				t.Fatal("hanging request succeeded")
			}
			select {
			case <-p.Done():
			case <-time.After(4 * time.Second):
				t.Fatal("request timeout left process alive")
			}
			if err = p.Close(); err != nil {
				t.Fatal(err)
			}
			// kill -0 is only used by this macOS fixture, not production process control.
			if err = exec.Command("/bin/kill", "-0", fmt.Sprint(result.Pid)).Run(); err == nil {
				t.Fatal("grandchild survived")
			}
			versions, err := os.ReadDir(filepath.Join(root, "runtime", "ru-agent-engines", "fixture", "agent"))
			if err != nil {
				t.Fatal(err)
			}
			if len(versions) != 0 {
				t.Fatal("process lease did not release after complete tree exit")
			}
		})
	}
}

func TestReverseRequestFloodIsBoundedAndCancelled(t *testing.T) {
	script := `require('node:readline').createInterface({input:process.stdin}).on('line',()=>{for(let i=0;i<50;i++)console.log(JSON.stringify({jsonrpc:'2.0',id:i,method:'elicitation/create',params:{sessionId:'s'}}));});`
	registry, root := fixture(t, script)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{MaxIncoming: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var calls atomic.Int32
	p, err := host.Start(context.Background(), "chat", "fixture/agent", root, func(ctx context.Context, _ driver.Incoming) (any, *sdk.RequestError) {
		calls.Add(1)
		<-ctx.Done()
		return nil, sdk.NewInternalError(nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = p.Request(ctx, "flood", nil); err == nil {
		t.Fatal("flood was accepted")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() > 2 {
		t.Fatalf("unbounded inbound handlers: %d", calls.Load())
	}
}

func TestEngineEnvironmentKeepsHomeButUsesPrivateProfileAndExplicitProxy(t *testing.T) {
	localBin := filepath.Join(t.TempDir(), "user-cli-bin")
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+localBin+string(os.PathListSeparator)+".")
	t.Setenv("AP_ACCESS_TOKEN", "host-secret")
	t.Setenv("NODE_OPTIONS", "--require /must-not-load")
	t.Setenv("NODE_PATH", "/must-not-resolve")
	t.Setenv("OPENAI_API_KEY", "personal-secret")
	script := `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:process.env}));});`
	registry, root := fixture(t, script)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{Proxy: httpclient.Config{Mode: "fixed", URL: "http://127.0.0.1:7890", Bypass: []string{"example.internal"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	p, err := host.Start(context.Background(), "chat", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := p.Request(ctx, "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	json.Unmarshal(data, &env)
	for _, key := range []string{"AP_ACCESS_TOKEN", "NODE_OPTIONS", "NODE_PATH", "OPENAI_API_KEY", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if _, ok := env[key]; ok {
			t.Fatalf("inherited forbidden %s", key)
		}
	}
	paths := strings.Split(env["PATH"], string(os.PathListSeparator))
	foundLocal := false
	for _, directory := range paths {
		if directory == localBin {
			foundLocal = true
		}
		if !filepath.IsAbs(directory) {
			t.Fatal("relative executable search path inherited")
		}
	}
	if !foundLocal {
		t.Fatal("user CLI search path was discarded")
	}
	if env["HOME"] != os.Getenv("HOME") || env["HTTPS_PROXY"] != "http://127.0.0.1:7890" || !strings.Contains(env["NO_PROXY"], "127.0.0.1") {
		t.Fatal("home or proxy policy not applied")
	}
	if !strings.HasPrefix(env["ZENMIND_AGENT_PROFILE"], filepath.Join(root, "runtime", "agent-engine-profiles")) || env["ZENMIND_AGENT_PROFILE"] == os.Getenv("ZENMIND_AGENT_PROFILE") {
		t.Fatal("personal profile inherited")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	p2, err := host.Start(context.Background(), "other-chat", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err = p2.Request(ctx, "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	var next map[string]string
	json.Unmarshal(data, &next)
	if next["ZENMIND_AGENT_PROFILE"] != env["ZENMIND_AGENT_PROFILE"] {
		t.Fatal("profile changed with process/chat")
	}
	if err = p2.Close(); err != nil {
		t.Fatal(err)
	}
	profile := env["ZENMIND_AGENT_PROFILE"]
	if files, err := os.ReadDir(profile); err != nil || len(files) != 0 {
		t.Fatal("host created product directories", files, err)
	}
	if err = os.Rename(profile, profile+"-original"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, profile); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Start(context.Background(), "escape", "fixture/agent", root, nil); err == nil || err.Error() != "profile_unavailable" {
		t.Fatal("linked profile accepted", err)
	}
}

func TestProcessAndPromptCapacityHaveBoundedQueueAndDoNotReplay(t *testing.T) {
	script := `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);if(!m.id)return;
 console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:m.params}));setTimeout(()=>console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{ok:true}})),200);});`
	registry, root := fixture(t, script)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{MaxProcesses: 3, MaxActive: 1, MaxQueued: 1, QueueTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	notices := make(chan string, 8)
	handler := func(_ context.Context, in driver.Incoming) (any, *sdk.RequestError) {
		var value struct{ Token string }
		json.Unmarshal(in.Params, &value)
		notices <- value.Token
		return nil, nil
	}
	p, err := host.Start(context.Background(), "a", "fixture/agent", root, handler)
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Start(context.Background(), "b", "fixture/agent", root, handler)
	if err != nil {
		t.Fatal(err)
	}
	third, err := host.Start(context.Background(), "c", "fixture/agent", root, handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Start(context.Background(), "d", "fixture/agent", root, nil); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("process capacity: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Request(context.Background(), "session/prompt", map[string]string{"token": "first"})
		done <- err
	}()
	select {
	case <-notices:
	case <-time.After(time.Second):
		t.Fatal("first prompt never started")
	}
	if _, err = p.Request(context.Background(), "session/prompt", nil); !errors.Is(err, driver.ErrBusy) {
		t.Fatal("parallel prompt on same Chat admitted")
	}
	queued := make(chan error, 1)
	go func() {
		_, err := second.Request(context.Background(), "session/prompt", map[string]string{"token": "must-not-send"})
		queued <- err
	}()
	deadline := time.Now().Add(time.Second)
	for host.Stats().Queued != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = third.Request(context.Background(), "session/prompt", nil); !errors.Is(err, driver.ErrBusy) {
		t.Fatalf("queue overflow: %v", err)
	}
	var callErr *driver.CallError
	if err = <-queued; !errors.Is(err, driver.ErrBusy) || !errors.As(err, &callErr) || callErr.ExecutionState != "not_started" {
		t.Fatalf("queued timeout: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case token := <-notices:
		t.Fatalf("replayed expired request: %s", token)
	default:
	}
	if _, err = second.Request(context.Background(), "session/prompt", map[string]string{"token": "next"}); err != nil {
		t.Fatal(err)
	}
	if stats := host.Stats(); stats.Active != 0 || stats.Queued != 0 || stats.Processes != 3 {
		t.Fatalf("leaked capacity: %+v", stats)
	}
}

func TestMalformedFramesAndStderrFloodStayBounded(t *testing.T) {
	script := `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);process.stderr.write('sensitive-token='.repeat(20000));
 const frames={noise:'not json',missing:'{"jsonrpc":"2.0","id":1}',duplicate:'{"jsonrpc":"2.0","id":1,"id":2,"result":{}}',oversize:'x'.repeat(1024*1024+1)};process.stdout.write(frames[m.params.kind]+'\n');});`
	registry, root := fixture(t, script)
	host, _ := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	defer host.Close()
	for _, kind := range []string{"noise", "missing", "duplicate", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			p, err := host.Start(context.Background(), kind, "fixture/agent", root, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err = p.Request(ctx, "bad", map[string]string{"kind": kind}); err == nil {
				t.Fatal("bad frame accepted")
			}
			if err = p.Close(); err != nil {
				t.Fatal(err)
			}
			stats := p.Diagnostics()
			if stats.StderrBytes == 0 || stats.StderrRetained > 64*1024 {
				t.Fatalf("stderr not bounded: %+v", stats)
			}
			if strings.Contains(string(encoded(t, stats)), "sensitive-token") {
				t.Fatal("diagnostics leaked raw stderr")
			}
		})
	}
}

func TestCancelNotificationCanPassAnActivePrompt(t *testing.T) {
	registry, root := fixture(t, `let pending; const send=m=>console.log(JSON.stringify(m)); require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line); if(m.method==='session/prompt'){pending=m.id;send({jsonrpc:'2.0',method:'session/update',params:{started:true}})} if(m.method==='session/cancel'&&!('id' in m))send({jsonrpc:'2.0',id:pending,result:{stopReason:'cancelled'}})});`)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{MaxActive: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	process, err := host.Start(ctx, "chat", "fixture/agent", root, func(context.Context, driver.Incoming) (any, *sdk.RequestError) {
		started <- struct{}{}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		value, err := process.Request(ctx, "session/prompt", map[string]string{"sessionId": "s"})
		if err == nil && !strings.Contains(string(value), "cancelled") {
			err = fmt.Errorf("unexpected response: %s", value)
		}
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = process.Notify(ctx, "session/cancel", map[string]string{"sessionId": "s"}); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if host.Stats().Active != 0 {
		t.Fatal("cancel did not release active slot")
	}
}

func TestBlockedStdinAndUnexpectedExitReleaseCapacity(t *testing.T) {
	for name, script := range map[string]string{
		"blocked stdin": `setInterval(()=>{},1000);`,
		"crash":         `require('node:readline').createInterface({input:process.stdin}).on('line',()=>process.exit(23));`,
		"eof":           `require('node:readline').createInterface({input:process.stdin}).on('line',()=>process.stdout.end());setInterval(()=>{},1000);`,
	} {
		t.Run(name, func(t *testing.T) {
			registry, root := fixture(t, script)
			host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{MaxProcesses: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			p, err := host.Start(context.Background(), "chat", "fixture/agent", root, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err = p.Request(ctx, "probe", strings.Repeat("x", 900*1024))
			if err == nil {
				t.Fatal("expected failed call")
			}
			select {
			case <-p.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("process did not stop")
			}
			if err = p.Close(); err != nil {
				t.Fatal(err)
			}
			if host.Stats().Processes != 0 {
				t.Fatal("capacity retained after cleanup")
			}
			q, err := host.Start(context.Background(), "chat", "fixture/agent", root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if q.Generation() == p.Generation() {
				t.Fatal("reused connection generation")
			}
			if _, err = p.Request(context.Background(), "stale", nil); !errors.Is(err, driver.ErrClosed) {
				t.Fatalf("old connection accepted call: %v", err)
			}
		})
	}
}

func TestBroadProfileDirectoryIsRejectedBeforeSpawn(t *testing.T) {
	registry, root := fixture(t, echoAgent)
	profileRoot := filepath.Join(root, "runtime", "agent-engine-profiles")
	if err := os.Mkdir(profileRoot, 0755); err != nil {
		t.Fatal(err)
	}
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if _, err = host.Start(context.Background(), "chat", "fixture/agent", root, nil); err == nil {
		t.Fatal("broad profile permissions accepted")
	}
	if host.Stats().Processes != 0 {
		t.Fatal("failed start retained process capacity")
	}
	if err = os.Chmod(profileRoot, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := host.Start(context.Background(), "chat", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}

func TestNotificationFloodCancelsBlockedConsumer(t *testing.T) {
	registry, root := fixture(t, `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);for(let n=0;n<(m.method==='flood'?100:1);n++)console.log(JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{n}}))});`)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	p, err := host.Start(context.Background(), "chat", "fixture/agent", root, func(ctx context.Context, _ driver.Incoming) (any, *sdk.RequestError) {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := p.Request(ctx, "probe", nil); result <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = p.Notify(ctx, "flood", nil); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err == nil {
		t.Fatal("flood accepted")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("notification consumer not cancelled")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReverseCancellationDoesNotCancelOutboundOrSendLateAnswer(t *testing.T) {
	registry, root := fixture(t, `let pending;const send=m=>console.log(JSON.stringify(m));require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);if(m.method==='probe'){pending=m.id;send({jsonrpc:'2.0',id:'approval/1',method:'session/request_permission',params:{sessionId:'s'}})}else if(m.method==='cancel_reverse'){send({jsonrpc:'2.0',method:'$/cancel_request',params:{requestId:'approval/1'}});setTimeout(()=>send({jsonrpc:'2.0',id:pending,result:{ok:true}}),50)}else if(m.id==='approval/1')process.exit(29)});`)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	p, err := host.Start(context.Background(), "chat", "fixture/agent", root, func(ctx context.Context, in driver.Incoming) (any, *sdk.RequestError) {
		if string(in.ID) != `"approval/1"` {
			t.Errorf("lost string id: %s", in.ID)
		}
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return map[string]bool{"mustNotSend": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := p.Request(ctx, "probe", nil); result <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = p.Notify(ctx, "cancel_reverse", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("reverse handler not cancelled")
	}
	if err = <-result; err != nil {
		t.Fatalf("outbound interrupted or late answer sent: %v", err)
	}
}

func TestUnsupportedProxyPolicyFailsBeforeSpawn(t *testing.T) {
	registry, root := fixture(t, echoAgent)
	for _, proxy := range []httpclient.Config{{Mode: "pac_auto"}, {Mode: "fixed", URL: "socks5://127.0.0.1:7890"}} {
		host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{Proxy: proxy})
		if err != nil {
			t.Fatal(err)
		}
		_, err = host.Start(context.Background(), "chat", "fixture/agent", root, nil)
		if err == nil || err.Error() != "proxy_policy_unsupported" {
			t.Fatalf("unsupported proxy silently accepted: %v", err)
		}
		if host.Stats().Processes != 0 {
			t.Fatal("failed start retained capacity")
		}
		host.Close()
	}
}

func TestPromptCancellationWaitsForUpstreamAndKeepsHealthyProcess(t *testing.T) {
	registry, root := fixture(t, `let pending; const send=m=>console.log(JSON.stringify(m)); require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line); if(m.method==='session/prompt'){pending=m.id;send({jsonrpc:'2.0',method:'session/update',params:{started:true}})} if(m.method==='session/cancel'&&!('id' in m))setTimeout(()=>send({jsonrpc:'2.0',id:pending,result:{stopReason:'cancelled'}}),100)});`)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{MaxActive: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	process, err := host.Start(ctx, "chat", "fixture/agent", root, func(context.Context, driver.Incoming) (any, *sdk.RequestError) {
		started <- struct{}{}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	result := make(chan error, 1)
	go func() {
		value, err := process.Request(turnCtx, "session/prompt", map[string]string{"sessionId": "s"})
		if err == nil && !strings.Contains(string(value), "cancelled") {
			err = fmt.Errorf("unexpected response: %s", value)
		}
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelTurn()
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
		t.Fatal("healthy process killed after soft cancel")
	default:
	}
	if host.Stats().Active != 0 {
		t.Fatal("cancel did not release active slot")
	}
}

func TestMain(m *testing.M) {
	if handled, code := processgroup.RunGuardian(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestEngineEnvironmentAutoSystemProxy(t *testing.T) {
	expected := os.Getenv("ACP_TEST_SYSTEM_HTTP_PROXY")
	if runtime.GOOS != "darwin" || expected == "" {
		t.Skip("requires an explicitly selected macOS system HTTP/HTTPS proxy")
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy"} {
		t.Setenv(key, "")
	}
	script := `require('node:readline').createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line);console.log(JSON.stringify({jsonrpc:'2.0',id:m.id,result:{http:process.env.HTTP_PROXY,https:process.env.HTTPS_PROXY,bypass:process.env.NO_PROXY}}));});`
	registry, root := fixture(t, script)
	host, err := driver.New(registry, filepath.Join(root, "runtime"), driver.Options{Proxy: httpclient.Config{Mode: "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := host.Start(ctx, "chat", "fixture/agent", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Request(ctx, "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	if err = json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env["http"] != expected || env["https"] != expected || !strings.Contains(env["bypass"], "127.0.0.1") {
		t.Fatal("system proxy not inherited by real ACP child")
	}
}

func TestExecutableAgentFixture(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-1] != "literal argument with spaces" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request struct {
			ID     json.RawMessage
			Method string
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(2)
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new":
			result["sessionId"] = "native-session"
		case "session/prompt":
			update, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Executable ACP answer"}}}})
			os.Stdout.Write(append(update, '\n'))
			result["stopReason"] = "end_turn"
		}
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		os.Stdout.Write(append(reply, '\n'))
	}
	os.Exit(0)
}
