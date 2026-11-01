//go:build linux

package webapphost_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
	"agent-platform/internal/webappgateway"
	"agent-platform/internal/webapphost"
	"github.com/gorilla/websocket"
)

//go:embed testdata/reference.cjs
var reference []byte

func nativeFixture(t *testing.T) (*webapp.Service, *webappgateway.Gateway, webapp.Entry, webapp.Options) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Getenv("WEBAPP_NATIVE_TEST") != "1" {
		t.Skip("requires hardened native linux/amd64 Compose runtime with locked Node/helper")
	}
	status := webapphost.Inspect(context.Background())
	if !status.BackendPrerequisitesReady {
		t.Fatalf("runtime prerequisites: %+v", status)
	}
	if err := os.WriteFile("/tmp/host-only-secret", []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove("/tmp/host-only-secret") })
	root := t.TempDir()
	cfg := config.DefaultWebAppsConfig()
	cfg.MaxArchiveMiB = 1
	cfg.MaxExtractedMiB = 8
	cfg.Logs.MaxSizeMB = 1
	cfg.Logs.MaxFiles = 2
	cfg.PlatformOrigin = "http://127.0.0.1:11948"
	cfg.Backend.HealthIntervalSeconds = 1
	cfg.Backend.UnhealthyThreshold = 1
	cfg.Backend.Restart.MaxAttempts = 2
	options := webapp.Options{Root: filepath.Join(root, "webapps"), DataRoot: filepath.Join(root, "data"), Config: cfg}
	options.Runner = webapphost.NewRunner(options.Root, cfg, true)
	s, err := webapp.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return err
		})
	})
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	manifest := `{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"reference-app","label":"Reference","version":"1.0.0","target":"linux-x64","frontend":{"root":"frontend","index":"index.html","routeConfig":{"backendPrefixes":["/api"]}},"backend":{"command":{"type":"electron-node","entry":"backend/main.cjs"},"health":{"type":"http","path":"/health","startupTimeoutMs":10000},"shutdownTimeoutMs":1000}}`
	for name, data := range map[string][]byte{"webapp.json": []byte(manifest), "frontend/index.html": []byte("<h1>reference</h1>"), "backend/main.cjs": reference} {
		f, _ := z.Create("webapp-0123456789abcdef/" + name)
		_, _ = f.Write(data)
	}
	z.Close()
	u, err := s.Upload(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Install(context.Background(), webapp.InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	e, err = s.Start(context.Background(), e.ID, webapp.StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	e = waitState(t, s, e.ID, "running")
	g := webappgateway.New(s, cfg)
	t.Cleanup(g.Close)
	return s, g, e, options
}
func waitState(t *testing.T, s *webapp.Service, id, state string) webapp.Entry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var e webapp.Entry
	for time.Now().Before(deadline) {
		e, _ = s.Get(id)
		if e.ObservedState == state {
			return e
		}
		if e.ObservedState == "blocked" || e.ObservedState == "failed" {
			page, _ := s.Logs(id, "stderr", "")
			t.Fatalf("state %+v logs %s", e, page.Text)
		}
		time.Sleep(50 * time.Millisecond)
	}
	page, _ := s.Logs(id, "stderr", "")
	t.Fatalf("timeout %+v logs %s", e, page.Text)
	return e
}
func openClient(t *testing.T, g *webappgateway.Gateway, id string) (*http.Client, string) {
	t.Helper()
	launch, err := g.Launch("owner", id, "/")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest("POST", launch.Action, strings.NewReader(url.Values{"ticket": {launch.Ticket}}.Encode()))
	r.Header.Set("Origin", "http://127.0.0.1:11948")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 {
		t.Fatal("launch", response.StatusCode)
	}
	return client, strings.TrimSuffix(launch.Action, "/__desktop/session/exchange")
}
func TestComposeNativeNodeProxyAndIsolation(t *testing.T) {
	s, g, e, _ := nativeFixture(t)
	client, origin := openClient(t, g, e.ID)
	response, err := client.Get(origin + "/api/probes")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(raw))
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"policyDenied", "otherDenied", "procDenied", "codeWriteDenied", "configWriteDenied", "spawnDenied", "platformSecretAbsent", "bindDenied", "connectDenied"} {
		if result[key] != true {
			t.Errorf("isolation %s failed: %s", key, raw)
		}
	}
	if result["data"] != "persistent" {
		t.Fatal(string(raw))
	}
	r, _ := http.NewRequest("GET", origin+"/api/echo", nil)
	r.Header.Set("X-Forwarded-Host", "forged")
	r.Header.Set("X-Agent-Owner", "forged")
	r.Header.Set("Authorization", "Bearer business-token")
	r.AddCookie(&http.Cookie{Name: "access_token", Value: "platform-secret"})
	r.AddCookie(&http.Cookie{Name: "business", Value: "business-cookie"})
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(raw), "platform-secret") || strings.Contains(string(raw), "ap_webapp_") || strings.Contains(string(raw), "forged") || !strings.Contains(string(raw), "business-token") || !strings.Contains(string(raw), "business-cookie") {
		t.Fatal("proxy credentials", string(raw))
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name != "business" {
			t.Fatal("reserved or domain cookie escaped", cookie)
		}
	}
	page, err := s.Logs(e.ID, "stdout", "")
	if err != nil || !strings.Contains(page.Text, "probes-complete") {
		t.Fatal("logs", page, err)
	}
}
func TestComposeNativeStopClosesWSAndSSEAndRestoresData(t *testing.T) {
	s, g, e, options := nativeFixture(t)
	client, origin := openClient(t, g, e.ID)
	response, err := client.Get(origin + "/api/sse")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "ready") {
		t.Fatal("SSE not flushed", line, err)
	}
	u, _ := url.Parse(origin)
	headers := http.Header{"Origin": {origin}}
	for _, c := range client.Jar.Cookies(u) {
		headers.Add("Cookie", c.String())
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+u.Host+"/api/ws", headers)
	if err != nil {
		t.Fatal(err)
	}
	_, message, err := ws.ReadMessage()
	if err != nil || string(message) != "ready" {
		t.Fatal("WS", string(message), err)
	}
	scope, err := s.Acquire(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := scope.DataRoot
	scope.Release()
	if err := os.WriteFile(filepath.Join(dataRoot, "retained"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped, err := s.Stop(ctx, e.ID, webapp.StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("WS survived stop")
	}
	ws.Close()
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err == nil {
		t.Fatal("SSE unexpectedly completed cleanly")
	}
	restarted, err := s.Start(ctx, e.ID, webapp.StateRequest{ExpectedRevision: stopped.Revision, ExpectedStateRevision: stopped.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	restarted = waitState(t, s, e.ID, "running")
	if restarted.Generation == e.Generation {
		t.Fatal("generation reused")
	}
	old, err := client.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	old.Body.Close()
	if old.StatusCode != 401 {
		t.Fatal("old session survived")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	options.Runner = webapphost.NewRunner(options.Root, options.Config, true)
	restored, err := webapp.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.RestoreDesired(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := waitState(t, restored, e.ID, "running")
	if fresh.Generation == restarted.Generation {
		t.Fatal("restore adopted generation")
	}
	raw, err := os.ReadFile(filepath.Join(dataRoot, "retained"))
	if err != nil || string(raw) != "keep" {
		t.Fatal("persistent data lost")
	}
}

func TestComposeNativeWatcherAndSupervisorDeathsDrainBeforeRestart(t *testing.T) {
	for _, which := range []string{"watcher", "supervisor"} {
		t.Run(which, func(t *testing.T) {
			s, _, e, _ := nativeFixture(t)
			children, err := allChildren(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(children))
			if len(fields) != 1 {
				t.Fatal("expected one watcher", string(children))
			}
			watcher, _ := strconv.Atoi(fields[0])
			victim := watcher
			if which == "supervisor" {
				children, err = os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", watcher, watcher))
				if err != nil {
					t.Fatal(err)
				}
				fields = strings.Fields(string(children))
				if len(fields) != 1 {
					t.Fatal("inner supervisor missing", string(children))
				}
				victim, _ = strconv.Atoi(fields[0])
			}
			if err := syscall.Kill(victim, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			var next webapp.Entry
			for time.Now().Before(deadline) {
				next, _ = s.Get(e.ID)
				if next.ObservedState == "running" && next.Generation != e.Generation {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if next.ObservedState != "running" || next.Generation == e.Generation {
				page, _ := s.Logs(e.ID, "stderr", "")
				t.Fatal("no clean restart", next, page.Text)
			}
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", victim)); !os.IsNotExist(err) {
				t.Fatal("old process survived", victim, err)
			}
		})
	}
}

func allChildren(pid int) ([]byte, error) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return nil, err
	}
	var result []byte
	for _, task := range tasks {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		if err != nil {
			return nil, err
		}
		result = append(result, raw...)
		result = append(result, ' ')
	}
	return result, nil
}
