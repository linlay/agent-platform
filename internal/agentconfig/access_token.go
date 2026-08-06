package agentconfig

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const maxAccessTokenFileBytes = 64 * 1024

// ReadAccessTokenFile reads the current Desktop identity token without
// caching it. A missing path or empty file means that no identity is active.
func ReadAccessTokenFile(filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", nil
	}
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open identity file: %w", err)
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxAccessTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read identity file: %w", err)
	}
	if len(contents) > maxAccessTokenFileBytes {
		return "", fmt.Errorf("identity file exceeds %d bytes", maxAccessTokenFileBytes)
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
