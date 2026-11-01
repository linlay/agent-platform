//go:build !linux && !darwin

package resources

import "os"

func openDependency(path string) (*os.File, error) { return os.Open(path) }
