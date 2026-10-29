package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSsoIdTokenFileFollowsCurrentFileState(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "sso-id-token.txt")
	assertToken := func(want string) {
		t.Helper()
		got, err := ReadSsoIdTokenFile(filePath)
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

func TestReadSsoIdTokenFileRejectsInvalidContent(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "sso-id-token.txt")
	for name, contents := range map[string][]byte{
		"multiple lines": []byte("token-a\ntoken-b\n"),
		"nul byte":       []byte("token-a\x00token-b"),
		"invalid utf8":   {0xff, 0xfe},
		"too large":      []byte(strings.Repeat("x", maxSsoIdTokenFileBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filePath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if token, err := ReadSsoIdTokenFile(filePath); err == nil || token != "" {
				t.Fatalf("ReadSsoIdTokenFile() = %q, %v; want invalid content error", token, err)
			}
		})
	}
}

func TestReadSsoIdTokenFileReturnsErrorForUnreadableTarget(t *testing.T) {
	if token, err := ReadSsoIdTokenFile(t.TempDir()); err == nil || token != "" {
		t.Fatalf("ReadSsoIdTokenFile(directory) = %q, %v; want read error", token, err)
	}
}

func TestIdentityEnvironmentIsFreshScopedAndRemovesInheritedValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sso-id-token.txt")
	t.Setenv(EnvIDToken, "process-value")
	original := []string{"KEEP=value", "ap_id_token=spoofed", "AP_ID_TOKEN=stale"}
	for _, token := range []string{"first", "second", ""} {
		if err := os.WriteFile(file, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		identity, err := ReadIdentityEnvironment(file)
		if err != nil {
			t.Fatal(err)
		}
		env := WithIdentityEnvironment(original, identity)
		want := []string{"KEEP=value"}
		if token != "" {
			want = append(want, EnvIDToken+"="+token)
		}
		if strings.Join(env, "|") != strings.Join(want, "|") {
			t.Fatal("incorrect identity environment")
		}
		if original[1] != "ap_id_token=spoofed" || os.Getenv(EnvIDToken) != "process-value" {
			t.Fatal("modified caller or process environment")
		}
	}
	if err := os.WriteFile(file, []byte("invalid\nlines"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := ReadIdentityEnvironment(file)
	if err == nil || len(WithIdentityEnvironment(original, identity)) != 1 {
		t.Fatal("invalid identity propagated")
	}
}
