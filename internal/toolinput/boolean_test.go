package toolinput

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseBoolExactValues(t *testing.T) {
	for _, tc := range []struct {
		in           any
		value, valid bool
	}{
		{true, true, true}, {false, false, true}, {"true", true, true}, {"false", false, true},
		{"TRUE", false, false}, {"False", false, false}, {" true", false, false}, {"false ", false, false},
		{"1", false, false}, {"0", false, false}, {"t", false, false}, {"yes", false, false}, {"", false, false},
		{nil, false, false}, {1, false, false}, {float64(0), false, false}, {[]any{"true"}, false, false},
	} {
		got, ok := ParseBool(tc.in)
		if got != tc.value || ok != tc.valid {
			t.Fatalf("%#v: %v %v", tc.in, got, ok)
		}
	}
}

func TestNormalizeSchemaBooleansScopedAndIdempotent(t *testing.T) {
	var schema, args map[string]any
	json.Unmarshal([]byte(`{"type":"object","properties":{"flag":{"type":"boolean"},"text":{"type":"string"},"opaque":{"type":"object"},"rows":{"type":"array","items":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}}}`), &schema)
	json.Unmarshal([]byte(`{"flag":"false","text":"true","opaque":{"flag":"true"},"unknown":"false","rows":[{"enabled":"true"},{"enabled":"TRUE"}]}`), &args)
	NormalizeSchemaBooleans(args, schema)
	want := `{"flag":false,"opaque":{"flag":"true"},"rows":[{"enabled":true},{"enabled":"TRUE"}],"text":"true","unknown":"false"}`
	raw, _ := json.Marshal(args)
	if string(raw) != want {
		t.Fatal(string(raw))
	}
	NormalizeSchemaBooleans(args, schema)
	raw, _ = json.Marshal(args)
	if string(raw) != want {
		t.Fatal("not idempotent", string(raw))
	}
	fields := map[string]string{"flag": "b!", "text": "s", "invalid": "b", "missing": "b"}
	v := map[string]any{"flag": "false", "text": "true", "invalid": "False"}
	NormalizeBooleanFields(v, fields)
	if !reflect.DeepEqual(v, map[string]any{"flag": false, "text": "true", "invalid": "False"}) {
		t.Fatal(v)
	}
}
