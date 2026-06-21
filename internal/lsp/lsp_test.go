package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestDetectLanguageID(t *testing.T) {
	tests := map[string]string{
		"main.go":    "go",
		"app.tsx":    "typescript",
		"index.mjs":  "javascript",
		"script.pyw": "python",
		"lib.rs":     "rust",
		"README.md":  "",
	}
	for path, want := range tests {
		if got := DetectLanguageID(path); got != want {
			t.Fatalf("DetectLanguageID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAfterFileChangeSkipsMissingServer(t *testing.T) {
	manager := NewManager(config.LSPDiagnosticsHookConfig{
		Enabled:   true,
		Timeout:   100,
		Languages: []string{"go"},
		Servers: map[string]config.LSPServerConfig{
			"go": {Command: "definitely-not-a-real-lsp-command"},
		},
	})

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		WorkspaceRoot: t.TempDir(),
		FilePath:      filepath.Join(t.TempDir(), "main.go"),
		Content:       []byte("package main\n"),
	})
	if result.Status != "skipped" || result.Reason != "server_not_found" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAfterFileChangeParsesDiagnosticsFromFakeServer(t *testing.T) {
	manager, cleanup := fakeManager(t, true, 1000)
	defer cleanup()
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		WorkspaceRoot: root,
		FilePath:      path,
		Content:       []byte("package main\n"),
	})
	if result.Status != "ok" {
		t.Fatalf("expected ok result, got %#v", result)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic, got %#v", result.Diagnostics)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.Severity != "error" || diagnostic.Message != "fake diagnostic" || diagnostic.Source != "fake-lsp" || diagnostic.Code != "E_FAKE" {
		t.Fatalf("unexpected diagnostic: %#v", diagnostic)
	}
	if diagnostic.Range.Start.Line != 1 || diagnostic.Range.Start.Character != 2 {
		t.Fatalf("unexpected diagnostic range: %#v", diagnostic.Range)
	}
}

func TestAfterFileChangeTimesOutWaitingForDiagnostics(t *testing.T) {
	manager, cleanup := fakeManager(t, false, 20)
	defer cleanup()
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		WorkspaceRoot: root,
		FilePath:      path,
		Content:       []byte("package main\n"),
	})
	if result.Status != "timeout" || result.Reason != "timeout" {
		t.Fatalf("unexpected timeout result: %#v", result)
	}
}

func fakeManager(t *testing.T, publishDiagnostics bool, timeoutSeconds int) (*Manager, func()) {
	t.Helper()
	manager := NewManager(config.LSPDiagnosticsHookConfig{
		Enabled:   true,
		Timeout:   timeoutSeconds,
		Languages: []string{"go"},
		Servers: map[string]config.LSPServerConfig{
			"go": {Command: "fake-lsp"},
		},
	})
	manager.lookPath = func(command string) (string, error) {
		return command, nil
	}
	var wg sync.WaitGroup
	manager.startProc = func(_ context.Context, _ string, _ []string, _ string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
		clientToServerReader, clientToServerWriter := io.Pipe()
		serverToClientReader, serverToClientWriter := io.Pipe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer serverToClientWriter.Close()
			runFakeServer(clientToServerReader, serverToClientWriter, publishDiagnostics)
		}()
		return nil, clientToServerWriter, serverToClientReader, nil
	}
	return manager, func() {
		_ = manager.Close()
		wg.Wait()
	}
}

func runFakeServer(in io.Reader, out io.Writer, publishDiagnostics bool) {
	reader := bufio.NewReader(in)
	latestURI := ""
	for {
		data, err := readLSPMessage(reader)
		if err != nil {
			return
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			continue
		}
		switch envelope.Method {
		case "initialize":
			writeFakeMessage(out, map[string]any{
				"jsonrpc": "2.0",
				"id":      envelope.ID,
				"result":  map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/didOpen":
			latestURI = uriFromTextDocument(envelope.Params, "textDocument")
		case "textDocument/didChange":
			latestURI = uriFromTextDocument(envelope.Params, "textDocument")
		case "textDocument/didSave":
			if uri := uriFromTextDocument(envelope.Params, "textDocument"); uri != "" {
				latestURI = uri
			}
			if publishDiagnostics && latestURI != "" {
				writeFakeMessage(out, map[string]any{
					"jsonrpc": "2.0",
					"method":  "textDocument/publishDiagnostics",
					"params": map[string]any{
						"uri": latestURI,
						"diagnostics": []map[string]any{{
							"range": map[string]any{
								"start": map[string]any{"line": 1, "character": 2},
								"end":   map[string]any{"line": 1, "character": 5},
							},
							"severity": 1,
							"source":   "fake-lsp",
							"code":     "E_FAKE",
							"message":  "fake diagnostic",
						}},
					},
				})
			}
		}
	}
}

func uriFromTextDocument(raw json.RawMessage, key string) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	textDocument, _ := payload[key].(map[string]any)
	uri, _ := textDocument["uri"].(string)
	return uri
}

func writeFakeMessage(out io.Writer, payload any) {
	data, _ := json.Marshal(payload)
	var message bytes.Buffer
	message.WriteString("Content-Length: ")
	message.WriteString(strconv.Itoa(len(data)))
	message.WriteString("\r\n\r\n")
	message.Write(data)
	_, _ = out.Write(message.Bytes())
}
