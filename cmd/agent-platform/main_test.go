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
	if _, err := parseConfigOptions([]string{"run"}); err == nil {
		t.Fatal("expected unexpected argument error")
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
