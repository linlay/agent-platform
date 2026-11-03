package main

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestParseConfigOptions(t *testing.T) {
	options, err := parseConfigOptions([]string{
		"--config-dir", "/tmp/config",
		"--port", "7078",
		"--sso-id-token-file", "/tmp/desktop state/sso-id-token.txt",
		"--runtime-mode=desktop",
	})
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if options.ConfigDir != "/tmp/config" {
		t.Fatalf("expected config dir, got %q", options.ConfigDir)
	}
	if options.Port != "7078" {
		t.Fatalf("expected port 7078, got %q", options.Port)
	}
	if options.SsoIdTokenFile != "/tmp/desktop state/sso-id-token.txt" {
		t.Fatalf("expected identity file to be preserved, got %q", options.SsoIdTokenFile)
	}
	if options.RuntimeMode != "desktop" {
		t.Fatalf("expected desktop runtime mode, got %q", options.RuntimeMode)
	}
}

func TestParseConfigOptionsLeavesSsoIdTokenFileUnsetByDefault(t *testing.T) {
	options, err := parseConfigOptions(nil)
	if err != nil {
		t.Fatalf("parse default options: %v", err)
	}
	if options.SsoIdTokenFile != "" {
		t.Fatalf("default identity file = %q, want empty", options.SsoIdTokenFile)
	}
	if options.RuntimeMode != "standalone" {
		t.Fatalf("default runtime mode = %q, want standalone", options.RuntimeMode)
	}
}

func TestRunHealthcheckCallsLoopbackHealthEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("health path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	t.Setenv("SERVER_PORT", port)
	if err := runHealthcheck(); err != nil {
		t.Fatalf("runHealthcheck: %v", err)
	}
}

func TestParseConfigOptionsRejectsRuntimeDir(t *testing.T) {
	if _, err := parseConfigOptions([]string{"--runtime-dir", "/tmp/runtime"}); err == nil {
		t.Fatal("expected runtime-dir flag error")
	}
}

func TestParseConfigOptionsRejectsUnexpectedArgs(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"connector-migrate", "--runtime-dir", "/tmp/runtime", "--apply"}} {
		if _, err := parseConfigOptions(args); err == nil {
			t.Fatalf("expected unexpected argument error for %v", args)
		}
	}
}

func TestMainImportsTzdataForWindowsAutomationZones(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(source), `_ "time/tzdata"`) {
		t.Fatalf("agent-platform must embed time/tzdata so Windows automation zoneId values such as Asia/Shanghai load without external zoneinfo")
	}
}

func TestParseConfigOptionsWebAppHost(t *testing.T) {
	options, err := parseConfigOptions([]string{"--webapp-host=compose"})
	if err != nil || options.WebAppHost != "compose" || options.RuntimeMode != "standalone" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	options, err = parseConfigOptions(nil)
	if err != nil || options.WebAppHost != "disabled" {
		t.Fatalf("default options=%+v err=%v", options, err)
	}
}
