package formhtml

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestHTMLPolicyCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, HTML string
		Valid      bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := Parse(tc.HTML)
			if (err == nil) != tc.Valid {
				t.Fatalf("valid=%v error=%v", tc.Valid, err)
			}
		})
	}
}

func TestValuesNormalizationAndUTF8Budget(t *testing.T) {
	controls, err := Parse(`<input name="days"><input type="checkbox" name="agree"><select name="s" multiple></select>`)
	if err != nil {
		t.Fatal(err)
	}
	values, err := NormalizeValues(map[string]any{"days": 3, "agree": true, "s": []any{2, false}}, controls)
	if err != nil || values["days"] != "3" || values["agree"] != "true" {
		t.Fatalf("%#v %v", values, err)
	}
	if values["s"].([]string)[1] != "false" {
		t.Fatal(values)
	}
	if _, err := NormalizeValues(nil, controls); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateData(map[string]any{"days": 3}, controls, false); err == nil {
		t.Fatal("submit must remain strict")
	}
	// JSON object overhead is 11 bytes; UTF-8 and HTML-sensitive characters count
	// by encoded bytes, without Go's optional HTML escaping.
	value := strings.Repeat("<", MaxBytes-11)
	if _, err := ValidateData(map[string]any{"days": value}, controls, false); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"x", "中", "\u2028"} {
		if _, err := ValidateData(map[string]any{"days": value + extra}, controls, false); err == nil {
			t.Fatal("accepted oversized data")
		}
	}
}
