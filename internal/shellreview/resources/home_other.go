//go:build !linux && !darwin

package resources

import "errors"

func accountHomeCommand(string) (string, []string) { return "", nil }
func parseAccountHome(string) (string, error)      { return "", errors.New("account home unsupported") }

func accountNotFound(error) bool { return false }
