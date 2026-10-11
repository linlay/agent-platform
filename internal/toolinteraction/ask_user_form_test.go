package toolinteraction

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/formhtml"
)

func formArgs(fragment string) map[string]any {
	return map[string]any{"title": "Profile", "html": fragment}
}

func TestAskUserFormHTMLValidation(t *testing.T) {
	valid := []string{
		`<label for="name">Name</label><input id="name" name="name" required>`,
		`<fieldset style="display:flex;gap:12px"><legend>Pick</legend><input type="radio" name="r" value="a"><input type="radio" name="r" value="b"></fieldset>`,
		`<input type="checkbox" name="c"><input type="checkbox" name="c"><select name="s" multiple><optgroup label="Group"><option>A</option></optgroup></select><textarea name="t"></textarea>`,
		`<table><tr><td><input name="n" type="number" min="1"></td></tr></table>`,
	}
	h := NewAskUserFormHandler()
	for _, fragment := range valid {
		if err := h.ValidateArgs(formArgs(fragment)); err != nil {
			t.Errorf("valid %s: %v", fragment, err)
		}
	}
	invalid := []string{
		`<script>alert(1)</script><input name="n">`, `<input name="n" onfocus="alert(1)">`, `<a href="x">link</a><input name="n">`,
		`<img src="x"><input name="n">`, `<iframe></iframe><input name="n">`, `<input name="n" type="file">`, `<input name="n" type="password">`,
		`<input name="n" type="submit">`, `<input>`, `<p>no controls</p>`, `<input name="n"><textarea name="n"></textarea>`,
		`<input type="radio" name="n"><input type="checkbox" name="n">`, `<input name="n" name="x">`,
		`<html><input name="n"></html>`, `<!doctype html><input name="n">`, `<svg><input name="n"></svg>`,
		`<input name="n" style="background-image:url(https://example.test)">`, `<input name="n" style="width:expression(alert(1))">`,
		`<input name="n" style="position: fixed">`, `<input name="n" style="color:u\72l(x)">`, `<input name="n" style="color:/**/red">`,
		`<input name="n" style="color:&#117;rl(x)">`, `<input name="n" formaction="https://example.test">`,
		strings.Repeat(" ", formhtml.MaxBytes) + `<input name="n">`,
	}
	for _, fragment := range invalid {
		if err := h.ValidateArgs(formArgs(fragment)); err == nil {
			t.Errorf("accepted %.150s", fragment)
		}
	}
	args := formArgs(`<input name="n">`)
	for _, values := range []any{"bad", map[string]any{"missing": "x"}, map[string]any{"n": map[string]any{}}} {
		args["values"] = values
		if err := h.ValidateArgs(args); err == nil {
			t.Errorf("accepted values %#v", values)
		}
	}
	args["values"] = map[string]any{"n": "Alice"}
	if err := h.ValidateArgs(args); err != nil {
		t.Fatal(err)
	}
	args["mode"] = "form"
	if h.ValidateArgs(args) == nil {
		t.Fatal("accepted unexpected argument")
	}
}

func TestAskUserFormNormalizeAndOutput(t *testing.T) {
	h := NewAskUserFormHandler()
	args := formArgs(`<input name="n"><input type="checkbox" name="yes"><input type="checkbox" name="many"><input type="checkbox" name="many"><select name="s" multiple></select>`)
	values := map[string]any{"n": "Alice", "yes": "false", "many": []any{"a", "b"}, "s": []any{}, "extra": "trim"}
	for _, decision := range []string{"approve", "reject"} {
		result, err := h.NormalizeSubmit(args, map[string]any{"decision": decision, "data": values, "reason": "later"})
		if err != nil {
			t.Fatal(err)
		}
		form := result["form"].(map[string]any)
		want := map[string]any{"n": "Alice", "yes": "false", "many": []string{"a", "b"}, "s": []string{}}
		if !reflect.DeepEqual(form["data"], want) {
			t.Fatalf("unexpected data %#v", form)
		}
		output := h.FormatModelOutput(contracts.ToolExecutionResult{Structured: result})
		if !strings.Contains(output, "Alice") || !strings.Contains(output, "Profile") || (decision == "reject" && !strings.Contains(output, "later")) {
			t.Fatal(output)
		}
		if _, ok := result["approval"]; ok {
			t.Fatal("form must never grant approval")
		}
	}
	result, err := h.NormalizeSubmit(args, map[string]any{"decision": "dismiss"})
	if err != nil || result["mode"] != "form" || result["status"] != "error" {
		t.Fatalf("dismiss %#v %v", result, err)
	}
	for _, param := range []any{
		[]any{}, map[string]any{"decision": "approve"}, map[string]any{"decision": "approve_rule_run", "data": map[string]any{}},
		map[string]any{"decision": "approve", "data": map[string]any{"n": 42}},
		map[string]any{"decision": "approve", "data": map[string]any{"n": map[string]any{}}},
		map[string]any{"decision": "approve", "data": map[string]any{"yes": "on"}},
		map[string]any{"decision": "approve", "data": map[string]any{"many": []any{true}}},
		map[string]any{"decision": "approve", "data": map[string]any{"s": "one"}},
		map[string]any{"decision": "reject", "data": map[string]any{"n": strings.Repeat("a", formhtml.MaxBytes)}},
	} {
		if _, err := h.NormalizeSubmit(args, param); err == nil {
			t.Fatalf("accepted %#v", param)
		}
	}
}

func TestAskUserFormAwaitAsk(t *testing.T) {
	h, ok := NewDefaultRegistry().Handler("ask_user_form")
	if !ok {
		t.Fatal("handler missing")
	}
	args := formArgs(`<input name="n">`)
	args["values"] = map[string]any{"n": "Alice"}
	ask := h.BuildInitialAwaitAsk("tool", "run", api.ToolDetailResponse{}, args, 0, 23)
	if ask == nil || ask.Mode != "form" || ask.View.Key != "ask_user_form" || ask.View.Renderer != "html" || ask.Timeout != 23 {
		t.Fatalf("bad ask %#v", ask)
	}
	if ask.Form["data"].(map[string]any)["html"] != args["html"] {
		t.Fatal("missing HTML")
	}
	if h.BuildInitialAwaitAsk("tool", "run", api.ToolDetailResponse{}, args, 1, 23) != nil {
		t.Fatal("duplicate ask")
	}
}

func TestAskUserFormCoercesDefaultsWithoutChangingArgs(t *testing.T) {
	h := NewAskUserFormHandler()
	for _, value := range []any{nil, map[string]any{"n": 3, "yes": true}} {
		args := formArgs(`<input name="n"><input name="yes" type="checkbox">`)
		args["values"] = value
		if err := h.ValidateArgs(args); err != nil {
			t.Fatal(err)
		}
		ask := h.BuildInitialAwaitAsk("tool", "run", api.ToolDetailResponse{}, args, 0, 600)
		if ask == nil {
			t.Fatal("no form")
		}
		if value != nil {
			values := ask.Form["data"].(map[string]any)["values"].(map[string]any)
			if values["n"] != "3" || values["yes"] != "true" {
				t.Fatal(values)
			}
			if args["values"].(map[string]any)["n"] != 3 {
				t.Fatal("args mutated")
			}
		}
	}
}
