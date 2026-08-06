package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAccessTokenFileFollowsCurrentFileState(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "sso-access-token.txt")
	assertToken := func(want string) {
		t.Helper()
		got, err := ReadAccessTokenFile(filePath)
		if err != nil {
			t.Fatalf("read token: %v", err)
		}
		if got != want {
			t.Fatalf("token = %q, want %q", got, want)
		}
	}

	assertToken("")
	if err := os.WriteFile(filePath, []byte(" \t\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertToken("")
	if err := os.WriteFile(filePath, []byte("token-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertToken("token-a")
	if err := os.WriteFile(filePath, []byte("token-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertToken("token-b")
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	assertToken("")
}

func TestReadAccessTokenFileRejectsInvalidContent(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "sso-access-token.txt")
	for name, contents := range map[string][]byte{
		"multiple lines": []byte("token-a\ntoken-b\n"),
		"nul byte":       []byte("token-a\x00token-b"),
		"invalid utf8":   {0xff, 0xfe},
		"too large":      []byte(strings.Repeat("x", maxAccessTokenFileBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filePath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if token, err := ReadAccessTokenFile(filePath); err == nil || token != "" {
				t.Fatalf("ReadAccessTokenFile() = %q, %v; want invalid content error", token, err)
			}
		})
	}
}

func TestReadAccessTokenFileReturnsErrorForUnreadableTarget(t *testing.T) {
	if token, err := ReadAccessTokenFile(t.TempDir()); err == nil || token != "" {
		t.Fatalf("ReadAccessTokenFile(directory) = %q, %v; want read error", token, err)
	}
}
