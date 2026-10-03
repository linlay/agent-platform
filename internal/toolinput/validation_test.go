package toolinput

import (
	"encoding/json"
	"strings"
	"testing"
)

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
