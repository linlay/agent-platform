package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Like the Desktop action contract check, local cross-repository development
// can require this with DESKTOP_SOURCE. It uses the actual private TS decoder
// in memory; no browser, emitted JS, Desktop service or external upload runs.
func TestBrowserFilesUploadWireAcceptedByDesktopDecoder(t *testing.T) {
	root := os.Getenv("DESKTOP_SOURCE")
	explicit := root != ""
	if !explicit {
		root = filepath.Join("..", "..", "..", "desktop")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "main", "support", "browser-files", "transfer.ts")); err != nil {
		if !explicit && os.IsNotExist(err) {
			t.Skip("Desktop checkout absent; set DESKTOP_SOURCE to require browser file wire compatibility")
		}
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required for the explicitly configured Desktop wire contract")
	}
	executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, nil)
	names := []any{"report.txt", "采购明细.csv", "data.json", "unknown-extension.fixture", ".gitignore", strings.Repeat("a", 201) + ".txt"}
	for _, raw := range names {
		mustWriteFile(t, filepath.Join(chatDir, raw.(string)), "fixture content\n")
	}
	args := browserUploadArgs()
	args["referenceNames"] = names
	result, err := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
	if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
		t.Fatalf("prepare upload wire: %#v %v", result, err)
	}
	files := invoker.requests[0].Payload["params"].(map[string]any)["files"]
	input, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	const script = `
const fs = require('node:fs'), path = require('node:path'), Module = require('node:module');
const root = process.argv[1], req = Module.createRequire(path.join(root, 'package.json'));
const ts = req('typescript'), file = path.join(root, 'src/main/support/browser-files/transfer.ts');
const mod = new Module(file); mod.paths = Module._nodeModulePaths(path.dirname(file));
mod._compile(ts.transpileModule(fs.readFileSync(file, 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, file);
const decoded = mod.exports.decodeBrowserUploadFiles(JSON.parse(fs.readFileSync(0, 'utf8')));
process.stdout.write(JSON.stringify(decoded.map(file => ({ name: file.name, mimeType: file.mimeType, sizeBytes: file.buffer.length }))));
`
	command := exec.Command(node, "-e", script, root)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Desktop rejected actual Go file wire; no browser was opened: %v %s", err, output)
	}
	var decoded []struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
		Size     int    `json:"sizeBytes"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil || len(decoded) != len(names) {
		t.Fatalf("invalid decoder receipt: %v %s", err, output)
	}
	for index, file := range decoded {
		if file.Name != names[index].(string) || file.Size != len("fixture content\n") || strings.ContainsAny(file.MimeType, ";\r\n") {
			t.Fatalf("wrong decoded metadata: %+v", file)
		}
	}
}
