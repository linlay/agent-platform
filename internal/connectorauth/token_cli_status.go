package connectorauth

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

// A process failure alone cannot tell whether upstream accepted the account.
// Only a complete machine-readable rejection or the conventional unauthorized
// status may reject a candidate; other failures leave it awaiting verification.
func tokenCLIStatusResult(cli map[string]any, stdout, stderr string, runErr error) (bool, error) {
	for _, stream := range []string{stdout, stderr} {
		var result struct {
			Error json.RawMessage `json:"error"`
			OK    *bool           `json:"ok"`
		}
		if json.Unmarshal([]byte(stream), &result) != nil {
			continue
		}
		if len(result.Error) != 0 && string(result.Error) != "null" {
			var failure struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(result.Error, &failure) == nil {
				switch failure.Code {
				case "AUTH_FAILED", "AUTH_REQUIRED", "UNAUTHORIZED", "INVALID_TOKEN", "CREDENTIALS_MISSING", "AUTHENTICATION_DENIED":
					return false, nil
				}
			}
			return false, ErrCredentialCheckFailed
		}
		if result.OK != nil && !*result.OK {
			return false, ErrCredentialCheckFailed
		}
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(stdout) == "unauthorized" {
			return false, nil
		}
		return false, ErrCredentialCheckFailed
	}
	return cliStatusMatches(cli, stdout, stderr), nil
}
