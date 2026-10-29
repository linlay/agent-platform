package agentconfig

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const maxSsoIdTokenFileBytes = 64 * 1024

// ReadIdentityEnvironment supplies the reserved, invocation-local SSO variable.
// Never merge this into os.Environ or persist it in a connector/runtime package.
func ReadIdentityEnvironment(filePath string) (map[string]string, error) {
	token, err := ReadSsoIdTokenFile(filePath)
	if err != nil || token == "" {
		return nil, err
	}
	return map[string]string{EnvAccessToken: token}, nil
}

// WithIdentityEnvironment removes inherited/spoofed SSO values before injecting
// a fresh identity snapshot. A nil snapshot deliberately removes the variable.
func WithIdentityEnvironment(base []string, identity map[string]string) []string {
	env := make([]string, 0, len(base)+1)
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if !strings.EqualFold(strings.TrimSpace(key), EnvAccessToken) {
			env = append(env, item)
		}
	}
	if token := identity[EnvAccessToken]; token != "" {
		env = append(env, EnvAccessToken+"="+token)
	}
	return env
}

// ReadSsoIdTokenFile reads the current Desktop identity token without
// caching it. A missing path or empty file means that no identity is active.
func ReadSsoIdTokenFile(filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", nil
	}
	file, err := openSsoIdTokenFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open identity file: %w", err)
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxSsoIdTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read identity file: %w", err)
	}
	if len(contents) > maxSsoIdTokenFileBytes {
		return "", fmt.Errorf("identity file exceeds %d bytes", maxSsoIdTokenFileBytes)
	}
	if !utf8.Valid(contents) {
		return "", fmt.Errorf("identity file must contain valid UTF-8")
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", nil
	}
	if strings.ContainsAny(token, "\r\n\x00") {
		return "", fmt.Errorf("identity file must contain one non-empty token line")
	}
	return token, nil
}
