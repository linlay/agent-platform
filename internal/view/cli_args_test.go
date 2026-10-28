package view

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func worklogBatchAction() Action {
	var action Action
	raw := `{"adapter":"cli","intercept":true,
	  "args":["--no-input","issue","worklog","add-batch",{"opt":"--payload","param":"payload","json":true},{"opt":"--limit","param":"limit"},{"flag":"--dry-run","param":"dryRun"},"--json"],
	  "paramsSchema":{"type":"object","additionalProperties":false,"required":["payload"],"properties":{
	    "payload":{"type":"object"},"limit":{"type":"integer"},"dryRun":{"type":"boolean"}}}}`
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		panic(err)
	}
	return action
}

func TestParseCLIArgsInvertsCLIArgs(t *testing.T) {
	action := worklogBatchAction()
	for _, params := range []map[string]any{
		{"payload": map[string]any{"entries": []any{map[string]any{"key": "DEMO-1", "comment": "a b"}}}},
		{"payload": map[string]any{}, "limit": json.Number("20"), "dryRun": true},
	} {
		argv, err := CLIArgs(action.Args, params)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseCLIArgs(action.Args, action.ParamsSchema, argv)
		if err != nil {
			t.Fatal(argv, err)
		}
		again, err := CLIArgs(action.Args, got)
		if err != nil || !reflect.DeepEqual(again, argv) {
			t.Fatalf("%v != %v (%v)", again, argv, err)
		}
	}
}

func TestParseCLIArgsAcceptsOtherSpellingsOfTheSameCall(t *testing.T) {
	action := worklogBatchAction()
	for _, argv := range [][]string{
		{"issue", "worklog", "add-batch", "--payload", `{"a":1}`},
		{"issue", "worklog", "add-batch", `--payload={"a":1}`, "--json", "--no-input"},
		{"--json", "issue", "worklog", "add-batch", "--limit", "5", "--payload", `{"a":1}`, "--dry-run"},
	} {
		params, err := ParseCLIArgs(action.Args, action.ParamsSchema, argv)
		if err != nil {
			t.Fatal(argv, err)
		}
		if _, ok := params["payload"].(map[string]any); !ok {
			t.Fatal(argv, params)
		}
	}
}

func TestParseCLIArgsRejectsWhatTheTemplateCannotPlace(t *testing.T) {
	action := worklogBatchAction()
	for _, argv := range [][]string{
		{"issue", "worklog", "add-batch", "--payload", `{}`, "--force"},
		{"issue", "worklog", "add-batch", "--payload", `{}`, "extra"},
		{"issue", "worklog", "add-batch", "--payload", `{}`, "--payload", `{}`},
		{"issue", "worklog", "add-batch", "--payload"},
		{"issue", "worklog", "add-batch", "--payload", `[1`},
		{"issue", "worklog", "add-batch", "--payload", `"text"`},
		{"issue", "worklog", "add-batch", "--limit", "five"},
		{"issue", "worklog", "add-batch", "--dry-run=true"},
		{"--profile", "prod", "issue", "worklog", "add-batch", "--payload", `{}`},
		{"issue", "worklog"},
	} {
		if _, err := ParseCLIArgs(action.Args, action.ParamsSchema, argv); !errors.Is(err, ErrDataInvalid) {
			t.Fatalf("%v: %v", argv, err)
		}
	}
}

func TestParseCLIArgsReadsPositionalParamsAfterThePath(t *testing.T) {
	args := []QueryArg{{Literal: "issue"}, {Literal: "worklog"}, {Literal: "add"}, {Param: "key"}, {Opt: "--date", Param: "date"}}
	params, err := ParseCLIArgs(args, nil, []string{"issue", "worklog", "add", "--date", "2026-10-27", "DEMO-1"})
	if err != nil || params["key"] != "DEMO-1" || params["date"] != "2026-10-27" {
		t.Fatal(params, err)
	}
}

func TestFindInterceptReadsParamsAndKeepsSchemaErrors(t *testing.T) {
	defs := map[string]Definition{"worklog": {Usage: []string{"display", "form"}, Actions: map[string]Action{"worklog.batch": worklogBatchAction()}}}
	if got := FindIntercept(defs, []string{"issue", "list"}); got != nil {
		t.Fatal(got)
	}
	got := FindIntercept(defs, []string{"issue", "worklog", "add-batch", "--payload", `{"x":1}`})
	if got == nil || got.Err != nil || got.ViewKey != "worklog" || got.Name != "worklog.batch" {
		t.Fatal(got)
	}
	// A partial call still opens the review; the person completes it there.
	got = FindIntercept(defs, []string{"issue", "worklog", "add-batch"})
	if got == nil || got.Err != nil || len(got.Params) != 0 {
		t.Fatal(got)
	}
	if err := ValidateParams(got.Action, got.Params); !errors.Is(err, ErrDataInvalid) {
		t.Fatalf("submit skipped required params: %v", err)
	}
	// Wrong types never reach the form.
	got = FindIntercept(defs, []string{"issue", "worklog", "add-batch", "--payload", `[1]`})
	if got == nil || !errors.Is(got.Err, ErrDataInvalid) {
		t.Fatal(got)
	}
}

func TestInterceptDeclarationRules(t *testing.T) {
	base := worklogBatchAction()
	for _, tc := range []struct {
		name   string
		usage  []string
		change func(*Action)
		valid  bool
	}{
		{"valid", []string{"display", "form"}, func(*Action) {}, true},
		{"display only", []string{"display"}, func(*Action) {}, false},
		{"stdin", []string{"display", "form"}, func(a *Action) {
			a.StdinParam = "payload"
		}, false},
		{"mcp", []string{"display", "form"}, func(a *Action) {
			a.Adapter, a.Args, a.Component, a.ToolName = "mcp", nil, "c", "t"
		}, false},
		{"no subcommand", []string{"display", "form"}, func(a *Action) {
			a.Args = []QueryArg{{Literal: "--json"}, {Opt: "--payload", Param: "payload", JSON: true}}
		}, false},
		{"literal after positional", []string{"display", "form"}, func(a *Action) {
			a.Args = []QueryArg{{Literal: "issue"}, {Param: "key"}, {Literal: "add"}}
			a.ParamsSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"key":{"type":"string"}}}`)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := base
			action.Args = append([]QueryArg(nil), base.Args...)
			tc.change(&action)
			err := validateActions("worklog", Definition{Usage: tc.usage, Actions: map[string]Action{"worklog.batch": action}})
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	duplicate := map[string]Definition{
		"a": {Actions: map[string]Action{"one": base}},
		"b": {Actions: map[string]Action{"two": base}},
	}
	if err := validateIntercepts(duplicate); err == nil || !strings.Contains(err.Error(), "issue worklog add-batch") {
		t.Fatal(err)
	}
}

func TestJSONArgsRequireObjectOrArrayParams(t *testing.T) {
	q := Query{Adapter: "cli", Args: []QueryArg{{Literal: "run"}, {Opt: "--payload", Param: "payload", JSON: true}},
		ParamsSchema: json.RawMessage(`{"type":"object","properties":{"payload":{"type":"string"}}}`)}
	if err := validateQueries("v", Definition{Usage: []string{"display"}, Queries: map[string]Query{"q": q}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := validateQueryArg(QueryArg{Param: "p", Flag: "--p", JSON: true}); err == nil {
		t.Fatal("json flag accepted")
	}
	argv, err := CLIArgs(q.Args, map[string]any{"payload": map[string]any{"a": []any{1.0, "x"}}})
	if err != nil || !reflect.DeepEqual(argv, []string{"run", "--payload", `{"a":[1,"x"]}`}) {
		t.Fatal(argv, err)
	}
	if _, err := CLIArgs(q.Args, map[string]any{"payload": "text"}); !errors.Is(err, ErrDataInvalid) {
		t.Fatal(err)
	}
}
