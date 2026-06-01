package tools

import (
	"testing"
	"time"
)

func TestBuildDateTimePayloadZeroOffsets(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 34, 56, 789, time.FixedZone("UTC+8", 8*60*60))
	tests := []struct {
		name   string
		offset string
	}{
		{name: "empty", offset: ""},
		{name: "plain zero", offset: "0"},
		{name: "positive zero", offset: "+0"},
		{name: "negative zero", offset: "-0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := buildDateTimePayload(map[string]any{"offset": tt.offset}, now)
			if err != nil {
				t.Fatalf("buildDateTimePayload() error = %v", err)
			}
			if got := payload["offset"]; got != "0" {
				t.Fatalf("payload offset = %v, want 0", got)
			}
			if got := payload["iso"]; got != "2026-06-01T12:34:56+08:00" {
				t.Fatalf("payload iso = %v, want current time without offset", got)
			}
		})
	}
}

func TestBuildDateTimePayloadRejectsInvalidOffsets(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC)
	tests := []string{"+0D0", "+"}

	for _, offset := range tests {
		t.Run(offset, func(t *testing.T) {
			_, err := buildDateTimePayload(map[string]any{"offset": offset}, now)
			if err == nil {
				t.Fatal("buildDateTimePayload() error = nil, want invalid offset error")
			}
		})
	}
}
