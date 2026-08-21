package tools

import (
	"fmt"
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

func TestBuildDateTimePayloadWithBase(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 34, 56, 0, time.FixedZone("UTC+8", 8*60*60))
	tests := []struct {
		name        string
		args        map[string]any
		wantBase    string
		wantISO     string
		wantDate    string
		wantTime    string
		wantZone    string
		wantWeekday string
	}{
		{
			name:        "date only defaults to midnight in timezone",
			args:        map[string]any{"base": "2026-08-16", "timezone": "Asia/Shanghai"},
			wantBase:    "2026-08-16T00:00:00+08:00",
			wantISO:     "2026-08-16T00:00:00+08:00",
			wantDate:    "2026-08-16",
			wantTime:    "00:00:00",
			wantZone:    "Asia/Shanghai",
			wantWeekday: "星期日",
		},
		{
			name:     "T separated local time interpreted in timezone",
			args:     map[string]any{"base": "2026-08-16T10:20:30", "timezone": "Asia/Shanghai"},
			wantBase: "2026-08-16T10:20:30+08:00",
			wantISO:  "2026-08-16T10:20:30+08:00",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "Asia/Shanghai",
		},
		{
			name:     "space separated local time",
			args:     map[string]any{"base": "2026-08-16 10:20:30", "timezone": "Asia/Shanghai"},
			wantBase: "2026-08-16T10:20:30+08:00",
			wantISO:  "2026-08-16T10:20:30+08:00",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "Asia/Shanghai",
		},
		{
			name:     "local wall time without timezone uses system local zone",
			args:     map[string]any{"base": "2026-08-16 10:20:30"},
			wantBase: "2026-08-16T10:20:30+08:00",
			wantISO:  "2026-08-16T10:20:30+08:00",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "UTC+8",
		},
		{
			name:     "UTC input keeps Z",
			args:     map[string]any{"base": "2026-08-16T10:20:30Z"},
			wantBase: "2026-08-16T10:20:30Z",
			wantISO:  "2026-08-16T10:20:30Z",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "Z",
		},
		{
			name:     "numeric offset input keeps offset",
			args:     map[string]any{"base": "2026-08-16T10:20:30-05:30"},
			wantBase: "2026-08-16T10:20:30-05:30",
			wantISO:  "2026-08-16T10:20:30-05:30",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "-05:30",
		},
		{
			name:     "explicit zero offset accepted and normalized to Z",
			args:     map[string]any{"base": "2026-08-16T10:20:30+00:00"},
			wantBase: "2026-08-16T10:20:30Z",
			wantISO:  "2026-08-16T10:20:30Z",
			wantDate: "2026-08-16",
			wantTime: "10:20:30",
			wantZone: "Z",
		},
		{
			name:     "embedded timezone converted to display timezone",
			args:     map[string]any{"base": "2026-08-16T10:00:00+02:00", "timezone": "Asia/Shanghai"},
			wantBase: "2026-08-16T10:00:00+02:00",
			wantISO:  "2026-08-16T16:00:00+08:00",
			wantDate: "2026-08-16",
			wantTime: "16:00:00",
			wantZone: "Asia/Shanghai",
		},
		{
			name:        "base plus chained offset applies in order",
			args:        map[string]any{"base": "2026-08-16", "timezone": "Asia/Shanghai", "offset": "+1D-2H"},
			wantBase:    "2026-08-16T00:00:00+08:00",
			wantISO:     "2026-08-16T22:00:00+08:00",
			wantDate:    "2026-08-16",
			wantTime:    "22:00:00",
			wantZone:    "Asia/Shanghai",
			wantWeekday: "星期日",
		},
		{
			name:        "cross month boundary",
			args:        map[string]any{"base": "2026-08-31", "timezone": "Asia/Shanghai", "offset": "+1D"},
			wantBase:    "2026-08-31T00:00:00+08:00",
			wantISO:     "2026-09-01T00:00:00+08:00",
			wantDate:    "2026-09-01",
			wantTime:    "00:00:00",
			wantZone:    "Asia/Shanghai",
			wantWeekday: "星期二",
		},
		{
			name:        "cross year boundary",
			args:        map[string]any{"base": "2026-12-31", "timezone": "Asia/Shanghai", "offset": "+1D"},
			wantBase:    "2026-12-31T00:00:00+08:00",
			wantISO:     "2027-01-01T00:00:00+08:00",
			wantDate:    "2027-01-01",
			wantTime:    "00:00:00",
			wantZone:    "Asia/Shanghai",
			wantWeekday: "星期五",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := buildDateTimePayload(tt.args, now)
			if err != nil {
				t.Fatalf("buildDateTimePayload() error = %v", err)
			}
			if got := payload["source"]; got != "input-base" {
				t.Fatalf("source = %v, want input-base", got)
			}
			if got := payload["base"]; got != tt.wantBase {
				t.Fatalf("base = %v, want %v", got, tt.wantBase)
			}
			if got := payload["iso"]; got != tt.wantISO {
				t.Fatalf("iso = %v, want %v", got, tt.wantISO)
			}
			if got := payload["date"]; got != tt.wantDate {
				t.Fatalf("date = %v, want %v", got, tt.wantDate)
			}
			if got := payload["time"]; got != tt.wantTime {
				t.Fatalf("time = %v, want %v", got, tt.wantTime)
			}
			if got := payload["timezone"]; got != tt.wantZone {
				t.Fatalf("timezone = %v, want %v", got, tt.wantZone)
			}
			if tt.wantWeekday != "" {
				if got := payload["weekday"]; got != tt.wantWeekday {
					t.Fatalf("weekday = %v, want %v", got, tt.wantWeekday)
				}
			}
		})
	}
}

func TestBuildDateTimePayloadRejectsInvalidBase(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC)
	tests := []any{
		"2026-8-16",
		"2026-08-1",
		"2026/08/16",
		"2026-02-29",
		"2026-13-01",
		"2026-00-10",
		"2026-08-16T10:20:30.123",
		"2026-08-16T10:20:30.123Z",
		"2026-08-16T10:20:30abc",
		"2026-08-16 ",
		" 2026-08-16",
		"2026-08-16T10:20",
		"2026-08-16T10:20:30+0800",
		"2026-08-16T10:20:30+8",
		"2026-08-16T25:00:00",
		"2026-08-16t10:20:30",
		"2026-08-16T10:20:30z",
		"yesterday",
		"now",
		20260816,
		true,
	}

	for _, base := range tests {
		t.Run(fmt.Sprint(base), func(t *testing.T) {
			_, err := buildDateTimePayload(map[string]any{"base": base}, now)
			if err == nil {
				t.Fatalf("buildDateTimePayload() error = nil, want invalid base error for %#v", base)
			}
		})
	}
}

func TestBuildDateTimePayloadWithoutBaseKeepsLegacyShape(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 34, 56, 0, time.FixedZone("UTC+8", 8*60*60))
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "no base key", args: map[string]any{"timezone": "Asia/Shanghai", "offset": "+1D"}},
		{name: "empty base", args: map[string]any{"base": "", "timezone": "Asia/Shanghai", "offset": "+1D"}},
		{name: "whitespace base", args: map[string]any{"base": "   ", "timezone": "Asia/Shanghai", "offset": "+1D"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := buildDateTimePayload(tt.args, now)
			if err != nil {
				t.Fatalf("buildDateTimePayload() error = %v", err)
			}
			if _, exists := payload["base"]; exists {
				t.Fatalf("payload must not contain base when base is absent: %#v", payload)
			}
			if got := payload["source"]; got != "system-clock" {
				t.Fatalf("source = %v, want system-clock", got)
			}
			if got := payload["timezone"]; got != "Asia/Shanghai" {
				t.Fatalf("timezone = %v, want Asia/Shanghai", got)
			}
			if got := payload["offset"]; got != "+1D" {
				t.Fatalf("offset = %v, want +1D", got)
			}
			if got := payload["iso"]; got != "2026-06-02T12:34:56+08:00" {
				t.Fatalf("iso = %v, want legacy value", got)
			}
		})
	}
}
