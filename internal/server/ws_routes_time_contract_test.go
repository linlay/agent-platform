package server

import (
	"math"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestJWTExpiryMillisConvertsOnlyValidNumericDateSeconds(t *testing.T) {
	value, err := jwtExpiryMillis(map[string]any{"exp": float64(1_700_000_000)})
	if err != nil || value != 1_700_000_000_000 {
		t.Fatalf("expected NumericDate seconds to convert to epoch ms, value=%d err=%v", value, err)
	}
	value, err = jwtExpiryMillis(map[string]any{})
	if err != nil || value != 0 {
		t.Fatalf("expected missing exp to remain absent, value=%d err=%v", value, err)
	}
	for name, claims := range map[string]map[string]any{
		"string":   {"exp": "1700000000"},
		"float":    {"exp": 1_700_000_000.5},
		"zero":     {"exp": float64(0)},
		"overflow": {"exp": float64(math.MaxInt64)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := jwtExpiryMillis(claims); !timecontract.IsViolation(err) {
				t.Fatalf("expected invalid exp violation, got %v", err)
			}
		})
	}
}
