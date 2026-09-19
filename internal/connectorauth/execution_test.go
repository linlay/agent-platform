package connectorauth

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestWindowsNodeShimUsesArgvNotCmd(t *testing.T) {
	for _, shim := range []string{`@node "%~dp0\launcher.cjs" %*`, `"%_prog%" "%dp0%\node_modules\demo\bin.js" %*`} {
		target, err := nodeShimScript(shim)
		if err != nil || target == "" {
			t.Fatal(target, err)
		}
	}
	if _, err := nodeShimScript(`echo not-a-node-shim`); err == nil {
		t.Fatal("accepted opaque shim")
	}
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	if runtime.GOOS == "windows" {
		node += ".exe"
	}
	os.WriteFile(node, []byte("fixture"), 0700)
	script := filepath.Join(dir, "launcher.cjs")
	os.WriteFile(script, []byte("fixture"), 0600)
	shim := filepath.Join(dir, "demo.cmd")
	os.WriteFile(shim, []byte(`@node "%~dp0\launcher.cjs" %*`), 0600)
	canonicalScript, _ := filepath.EvalSymlinks(script)
	args := []string{"--json", `{"text":"%PATH% & | $(touch nope) 中文"}`}
	cmd, err := executionCommand(context.Background(), shim, args, dir, []string{"PATH=" + dir}, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Args, append([]string{node, canonicalScript}, args...)) {
		t.Fatal(cmd.Args)
	}
	cmd, err = executionCommand(context.Background(), filepath.Join(dir, "demo.exe"), args, dir, nil, "windows")
	if err != nil || !reflect.DeepEqual(cmd.Args[1:], args) {
		t.Fatal(cmd, err)
	}
	cmd, err = executionCommand(context.Background(), filepath.Join(dir, "demo"), args, dir, nil, "darwin")
	if err != nil || !reflect.DeepEqual(cmd.Args[1:], args) {
		t.Fatal(cmd, err)
	}
}
