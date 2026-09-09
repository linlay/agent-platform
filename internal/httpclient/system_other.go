//go:build !darwin && !windows

package httpclient

import "context"

func readSystemSettings(context.Context) (systemSettings, error) { return systemSettings{}, nil }
