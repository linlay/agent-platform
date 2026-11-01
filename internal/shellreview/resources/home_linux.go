package resources

import (
	"errors"
	"os/exec"
)

func accountHomeCommand(uid string) (string, []string) {
	return "/usr/bin/getent", []string{"passwd", uid}
}
func parseAccountHome(raw string) (string, error) { return parsePasswdHome(raw) }

func accountNotFound(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 2
}
