package toolinput

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNullableFieldDoesNotRelaxNumberValidation(t *testing.T) {
	for _, value := range []any{nil, float64(3)} {
		if err := Validate(map[string]any{"count": value}, map[string]string{"count": "n?"}, "args."); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []any{float64(0), float64(101), float64(1.5), "3"} {
		if err := Validate(map[string]any{"count": value}, map[string]string{"count": "n?"}, "args."); err == nil {
			t.Fatalf("invalid number accepted: %v", value)
		}
	}
	if err := Validate(map[string]any{"count": nil}, map[string]string{"count": "n"}, "args."); err == nil {
		t.Fatal("null accepted by a non-nullable field")
	}
}

func TestDiagnosticsNeverEchoValuesOrUnknownKeys(t *testing.T) {
	for _, body := range []string{`{"token":"TOP-SECRET"}`, `{"token":{"TOP-SECRET":"TOP-SECRET"}}`, `{"TOP-SECRET":"TOP-SECRET"}`} {
		var v map[string]any
		_ = json.Unmarshal([]byte(body), &v)
		err := Validate(v, map[string]string{"token": "b!"}, "args.")
		if err == nil || strings.Contains(err.Error(), "TOP-SECRET") {
			t.Fatalf("%v", err)
		}
		b, _ := json.Marshal(err.(*Error).Details())
		if strings.Contains(string(b), "TOP-SECRET") {
			t.Fatal(string(b))
		}
	}
}
func TestValidationOrderStable(t *testing.T) {
	for i := 0; i < 30; i++ {
		err := Validate(map[string]any{"b": false, "a": false}, map[string]string{"a": "s!", "b": "s!"}, "")
		if err.(*Error).Field != "a" {
			t.Fatal(err)
		}
	}
}

func TestTypeDiagnosticsDoNotReplaceInputValues(t *testing.T) {
	for _, tc := range []struct {
		name, field, rule, body, expected string
	}{
		{"boolean", "enabled", "b!", `{"enabled":"false"}`, "JSON boolean"},
		{"integer", "limit", "n", `{"limit":"10"}`, "JSON integer in range 1–100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var values map[string]any
			if err := json.Unmarshal([]byte(tc.body), &values); err != nil {
				t.Fatal(err)
			}
			err := Validate(values, map[string]string{tc.field: tc.rule}, "args.")
			input, ok := err.(*Error)
			if !ok || input.Field != "args."+tc.field || input.Expected != tc.expected || input.Actual != "string" {
				t.Fatalf("incorrect type diagnostic: %v", err)
			}
			if strings.Contains(input.Recovery, "Set ") || strings.Contains(input.Recovery, "example:") || !strings.Contains(input.Recovery, tc.expected) {
				t.Fatalf("recovery should require the type without choosing a value: %s", input.Recovery)
			}
			if !strings.Contains(input.Error(), input.Field) || !strings.Contains(input.Error(), tc.expected) || !strings.Contains(input.Error(), "string") {
				t.Fatal(input.Error())
			}
			encoded, err := json.Marshal(values)
			if err != nil || string(encoded) != tc.body {
				t.Fatalf("validation changed the input: %s %v", encoded, err)
			}
		})
	}
	if err := Validate(map[string]any{"enabled": false, "limit": float64(10)}, map[string]string{"enabled": "b!", "limit": "n"}, "args."); err != nil {
		t.Fatalf("valid false and integer rejected: %v", err)
	}
}
