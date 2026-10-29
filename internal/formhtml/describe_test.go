package formhtml

import "testing"

func TestDescribeUsesValidatedControlsAndOptions(t *testing.T) {
	fields, err := Describe(`<input type="number" name="days"><select name="region" multiple><option value="cn">China</option><option>Europe</option></select><input type="checkbox" name="topics" value="a"><input type="checkbox" name="topics" value="b">`)
	if err != nil || len(fields) != 3 {
		t.Fatalf("%#v %v", fields, err)
	}
	if fields[0].Type != "number" || !fields[1].Multiple || len(fields[1].Options) != 2 || fields[1].Options[1] != "Europe" || !fields[2].Multiple || len(fields[2].Options) != 2 {
		t.Fatalf("%#v", fields)
	}
	if _, err := Describe(`<script>alert(1)</script>`); err == nil {
		t.Fatal("unsafe HTML described")
	}
}
