package resources

import (
	"errors"
	"strings"
)

func accountHomeCommand(uid string) (string, []string) {
	return "/usr/bin/dscacheutil", []string{"-q", "user", "-a", "uid", uid}
}
func parseAccountHome(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && key == "dir" {
			home := strings.TrimSpace(value)
			if strings.HasPrefix(home, "/") {
				return home, nil
			}
		}
	}
	return "", errors.New("invalid account home response")
}

func accountNotFound(error) bool { return false }
