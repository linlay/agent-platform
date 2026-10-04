package server

import (
	"agent-platform/internal/runtime/runexec"
)

// Remaining response collectors and public usage mappers share runtime helpers.
func floatValue(value any) float64 {
	return runexec.FloatValue(value)
}
