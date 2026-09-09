package httpclient

import (
	"context"
	"errors"
	"os/exec"
)

func readSystemSettings(ctx context.Context) (systemSettings, error) {
	// Fixed OS path, no shell, bounded execution. scutil is shipped with macOS
	// and reads SystemConfiguration's effective global proxy dictionary.
	output, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemSettings{}, errors.New("scutil --proxy failed or timed out")
	}
	return parseDarwinSettings(string(output))
}
