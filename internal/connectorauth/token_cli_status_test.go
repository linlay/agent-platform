package connectorauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCandidateCLIPrivateDirectoriesWindowsAndDarwin(t *testing.T) {
	root := t.TempDir()
	for _, goos := range []string{"windows", "darwin"} {
		dirs := candidateCLIPrivateDirectories(root, goos)
		if dirs["HOME"] != filepath.Join(root, "home") || dirs["USERPROFILE"] != dirs["HOME"] {
			t.Fatal("candidate home is not isolated", goos)
		}
		if goos == "windows" {
			if dirs["APPDATA"] != filepath.Join(root, "config") || dirs["LOCALAPPDATA"] != filepath.Join(root, "data") {
				t.Fatal("Windows config environment missing", dirs)
			}
		} else if dirs["APPDATA"] != "" || dirs["LOCALAPPDATA"] != "" {
			t.Fatal("Windows variables added to macOS candidate")
		}
		for _, dir := range dirs {
			if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatal("candidate path escaped temporary root", goos)
			}
		}
	}
}

func TestTokenCLIExecutionFailurePreservesAccountAndCandidate(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		exitCode     int
		rejected     bool
	}{
		{"missing APPDATA", `{"schema_version":1,"error":{"code":"INTERNAL_ERROR","message":"%AppData% is not defined"}}`, 1, false},
		{"internal error exit zero", `{"schema_version":1,"error":{"code":"INTERNAL_ERROR"}}`, 0, false},
		{"network failure", `{"error":{"code":"NETWORK_ERROR"}}`, 5, false},
		{"unknown process failure", `failed to start local configuration`, 2, false},
		{"credentials rejected", `{"schema_version":1,"error":{"code":"AUTH_FAILED"}}`, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, pkg := tokenCLIFixture(t, true)
			if _, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"}); err != nil {
				t.Fatal(err)
			}
			// Freeze a new status implementation, preserving the active account.
			script := filepath.Join(t.TempDir(), "demo.js")
			output, _ := json.Marshal(tc.output)
			body := "if(process.argv[2]==='--version'){console.log('1.2.0')}else{console.log(" + string(output) + ");process.exit(" + fmt.Sprint(tc.exitCode) + ")}"
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			testNodeCLI(t, script)
			if _, err := m.Prepare(t.Context(), pkg.ID); err != nil {
				t.Fatal(err)
			}
			result, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "candidate-secret"})
			if tc.rejected {
				if !errors.Is(err, ErrTokenRejected) {
					t.Fatal("explicit rejection was not rejected", result, err)
				}
			} else if err != nil || result.Status != "pending_verification" {
				t.Fatal("execution failure was treated as invalid credentials", result, err)
			}
			values, ready, err := TokenValues(pkg)
			if err != nil || !ready || values["API_KEY"] != "good-secret" {
				t.Fatal("probe failure changed active credentials", err)
			}
			pending, exists, err := pendingTokenValues(pkg)
			if err != nil || exists == tc.rejected || exists && pending["API_KEY"] != "candidate-secret" {
				t.Fatal("wrong candidate persistence", exists, err)
			}
		})
	}
}

func TestWindowsTokenCandidateUsesPrivateAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows process environment integration")
	}
	m, pkg := tokenCLIFixture(t, true)
	script := filepath.Join(t.TempDir(), "demo.js")
	body := `const fs=require('fs'),path=require('path');if(process.argv[2]==='--version'){console.log('1.2.0');process.exit(0)}const base=path.dirname(process.env.USERPROFILE);if(process.env.APPDATA!==path.join(base,'config')||process.env.LOCALAPPDATA!==path.join(base,'data')||!fs.statSync(process.env.APPDATA).isDirectory()||!fs.statSync(process.env.LOCALAPPDATA).isDirectory()){console.log(JSON.stringify({error:{code:'INTERNAL_ERROR'}}));process.exit(1)}console.log(JSON.stringify({authenticated:process.env.API_KEY==='good-secret'}));`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	if _, err := m.Prepare(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	session, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"})
	if err != nil || session.Status != "authorized" {
		t.Fatal("Windows candidate could not use private AppData", session, err)
	}
}
