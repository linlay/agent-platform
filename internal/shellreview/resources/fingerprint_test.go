package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func TestContainerFingerprintNeverBorrowsHostContent(t *testing.T) {
	// This name exists on the Host as well as in the hypothetical guest.
	// Missing guest evidence must not turn the Host bytes into guest evidence.
	file := filepath.Join(t.TempDir(), "script.sh")
	content := []byte("host-only script content")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hostHash := hex.EncodeToString(sum[:])
	failed := &types.BashEnvironment{Inspect: func(string) (string, string, error) {
		return "", "", errors.New("guest probe failed")
	}}
	var inspected string
	guest := &types.BashEnvironment{Inspect: func(target string) (string, string, error) {
		inspected = target
		return "guest header", "guest-only-digest", nil
	}}
	for _, tc := range []struct {
		name    string
		sandbox bool
		env     *types.BashEnvironment
		want    string
	}{
		{"host_without_environment", false, nil, hostHash},
		{"host_without_inspector", false, &types.BashEnvironment{}, hostHash},
		{"guest_without_environment", true, nil, ""},
		{"guest_without_inspector", true, &types.BashEnvironment{}, ""},
		{"guest_failed_probe", true, failed, ""},
		{"guest_verified_probe", true, guest, "guest-only-digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspected = ""
			version := ExecutionFingerprint(tc.sandbox, ExecutionVersion{Fingerprint: "seed", RuleKey: "opaque", AccessLevel: "ask_for_approval"}, types.BashExecution{Script: file, Cwd: filepath.Dir(file), Identity: "sh"}, nil, tc.env)
			if version.ContentSHA256 != tc.want {
				t.Fatalf("content digest = %q, want %q", version.ContentSHA256, tc.want)
			}
			if tc.sandbox && version.Scope != file {
				t.Fatalf("guest scope was resolved against the Host: %q", version.Scope)
			}
			if tc.env == guest && inspected != file {
				t.Fatalf("guest probe received a Host-normalized target: %q", inspected)
			}
		})
	}
}
