package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func statusProbeFixture(t *testing.T) (*Manager, connector.Package, string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node required for real CLI status")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"] = osCommands("demo login")
	cli["status"] = osCommands("demo status")
	cli["unAuth"] = osCommands("demo logout")
	cli["statusMatch"] = `^authorized\s*$`
	pkg := writeCLIPackage(t, root, "demo", cli)
	script := filepath.Join(t.TempDir(), "status.cjs")
	code := `const fs=require('fs'),p=require('path'),home=process.env.HOME;
if(process.argv[2]==='--version'){console.log('1.2.3');process.exit(0)}
if(process.argv[2]==='status'){fs.appendFileSync(p.join(home,'probes'),'x');setTimeout(()=>console.log(fs.existsSync(p.join(home,'authorized'))?'authorized':'unauthorized'),250)}
`
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script, pkg)
	manager := New(t.Context(), connector.Sources{ExternalRoot: root}, nil).ForOwner("user:status-probe")
	if _, err := manager.Prepare(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	pkg, err := manager.sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := pkg.UserStateDir()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "authorized"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := pkg.UpdateConnection(&yes, &yes); err != nil {
		t.Fatal(err)
	}
	return manager, pkg, home
}

func TestConcurrentConnectionReadsShareActualCLIWithoutReadyCache(t *testing.T) {
	manager, _, home := statusProbeFixture(t)
	start := make(chan struct{})
	slots := make(chan struct{}, 8)
	results := make(chan Connection, 16)
	failures := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			slots <- struct{}{}
			defer func() { <-slots }()
			state, err := manager.Connection(t.Context(), "demo")
			if err != nil {
				failures <- err
			} else {
				results <- state
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	count := 0
	for state := range results {
		count++
		if state.Readiness != "ready" || state.Authentication.Status != "authorized" {
			t.Fatalf("concurrent read flickered: %+v", state)
		}
	}
	if count != 16 {
		t.Fatal(count)
	}
	calls, err := os.ReadFile(filepath.Join(home, "probes"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) > 4 {
		t.Fatalf("concurrent status was not coalesced: %d actual subprocesses", len(calls))
	}
	if err := os.Remove(filepath.Join(home, "authorized")); err != nil {
		t.Fatal(err)
	}
	state, err := manager.Connection(t.Context(), "demo")
	if err != nil || state.Authentication.Status != "unauthorized" || state.Readiness != "authorization_required" {
		t.Fatal("stale ready result masked credential invalidation", state, err)
	}
}

func TestStatusProbePreservesMutationLockAndCancelableWait(t *testing.T) {
	manager, pkg, home := statusProbeFixture(t)
	done := make(chan error, 1)
	go func() { _, err := manager.Status(t.Context(), "demo"); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, _ := os.ReadFile(filepath.Join(home, "probes")); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CLI did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if release, err := connector.AcquireOperation(filepath.Dir(pkg.Dir), "demo"); !errors.Is(err, connector.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatal("mutation entered active status probe", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manager.Status(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled observer did not stop", err)
	}
	if err := <-done; err != nil {
		t.Fatal("canceling observer canceled another caller", err)
	}
	release, err := connector.AcquireOperation(filepath.Dir(pkg.Dir), "demo")
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Status(t.Context(), "demo")
	if !errors.Is(err, connector.ErrBusy) || state.Status == "authorized" {
		release()
		t.Fatal("real mutation lock became a cached ready/false login result", state, err)
	}
	release()
	if state, err = manager.Status(t.Context(), "demo"); err != nil || state.Status != "authorized" {
		t.Fatal("did not recover after actual mutation ended", state, err)
	}
}

func TestStatusProbeOwnerIsolation(t *testing.T) {
	manager, pkg, home := statusProbeFixture(t)
	other := New(t.Context(), connector.Sources{ExternalRoot: filepath.Dir(pkg.Dir)}, nil).ForOwner("user:other")
	otherPkg, _ := other.sources.Load("demo")
	dir, _ := otherPkg.UserStateDir()
	os.MkdirAll(filepath.Join(dir, "home"), 0700)
	if state, err := manager.Status(t.Context(), "demo"); err != nil || state.Status != "authorized" {
		t.Fatal(state, err)
	}
	if state, err := other.Status(t.Context(), "demo"); err != nil || state.Status != "unauthorized" {
		t.Fatal("shared another owner's authentication", state, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "probes"))
	if strings.TrimSpace(string(data)) != "x" {
		t.Fatal("unexpected original-owner probe", string(data))
	}
}

func TestCancelOneJoinedStatusWaiterKeepsOtherReadersAlive(t *testing.T) {
	manager, _, _ := statusProbeFixture(t)
	first := make(chan error, 1)
	go func() {
		state, err := manager.Status(t.Context(), "demo")
		if err == nil && state.Status != "authorized" {
			err = errors.New("first reader did not observe authorized")
		}
		first <- err
	}()
	ctx, cancel := context.WithCancel(t.Context())
	second := make(chan error, 1)
	go func() { _, err := manager.Status(ctx, "demo"); second <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		job := manager.cliStatusChecks["demo"]
		joined := job != nil && job.waiters >= 2
		manager.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readers did not join a shared probe")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatal("canceled observer kept waiting", err)
	}
	if err := <-first; err != nil {
		t.Fatal("another observer was canceled", err)
	}
}

func TestStatusProbeCannotPublishAcrossDisconnectOrReauth(t *testing.T) {
	for _, reauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect_epoch", true: "replacement_login"}[reauth], func(t *testing.T) {
			manager, _, home := statusProbeFixture(t)
			result := make(chan error, 1)
			go func() {
				state, err := manager.Status(t.Context(), "demo")
				if err == nil && state.Status == "authorized" {
					result <- errors.New("retired probe published authorized")
					return
				}
				result <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if data, _ := os.ReadFile(filepath.Join(home, "probes")); len(data) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("probe did not start")
				}
				time.Sleep(time.Millisecond)
			}
			if reauth {
				manager.mu.Lock()
				manager.sessions["demo"] = &login{Session: Session{ID: "replacement", Status: "pending"}}
				manager.mu.Unlock()
			} else {
				manager.retire("demo")
			}
			if err := <-result; !errors.Is(err, connector.ErrBusy) {
				t.Fatal("stale status crossed mutation generation", err)
			}
		})
	}
}
