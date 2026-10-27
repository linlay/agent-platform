package view

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMarkdownBlocksSharedCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "markdown_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name   string `json:"name"`
			Text   string `json:"text"`
			Final  bool   `json:"final"`
			Blocks []struct {
				Index int             `json:"index"`
				Key   string          `json:"key"`
				Data  json.RawMessage `json:"data"`
				Error string          `json:"error"`
			} `json:"blocks"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			blocks := ParseMarkdownBlocks(tc.Text, tc.Final)
			if len(blocks) != len(tc.Blocks) {
				t.Fatalf("blocks = %#v, want %d", blocks, len(tc.Blocks))
			}
			for i, want := range tc.Blocks {
				got := blocks[i]
				key := ""
				if got.Reference != nil {
					key = got.Reference.ConnectorID + "/" + got.Reference.Key
				}
				if got.Index != want.Index || key != want.Key || ErrorCode(got.Err) != want.Error {
					t.Fatalf("block %d = index %d key %q err %v, want %+v", i, got.Index, key, got.Err, want)
				}
				if len(want.Data) > 0 && !sameJSON(t, got.Data, want.Data) {
					t.Fatalf("block %d data = %s, want %s", i, got.Data, want.Data)
				}
			}
		})
	}
}

func TestParseMarkdownBlocksRejectsOversizedBody(t *testing.T) {
	body := `{"x":"` + strings.Repeat("a", MaxMarkdownDataBytes) + `"}`
	blocks := ParseMarkdownBlocks("```view key=a/b\n"+body+"\n```", true)
	if len(blocks) != 1 || !errors.Is(blocks[0].Err, ErrDataInvalid) {
		t.Fatalf("oversized body: %#v", blocks)
	}
}

func TestValidateInputUsesInputSchema(t *testing.T) {
	d := Definition{InputSchema: json.RawMessage(`{"type":"object","required":["jql"],"properties":{"jql":{"type":"string"}}}`)}
	if err := ValidateInput(d, json.RawMessage(`{"jql":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput(d, json.RawMessage(`{"jql":1}`)); !errors.Is(err, ErrDataInvalid) {
		t.Fatalf("schema violation: %v", err)
	}
}

func TestDisplayViewModelContractValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "views"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "views", "index.html"), []byte("<p>x</p>"), 0644); err != nil {
		t.Fatal(err)
	}
	base := Definition{Renderer: "html", Entry: "views/index.html"}
	input := json.RawMessage(`{"type":"object"}`)
	cases := map[string]struct {
		mutate func(*Definition)
		ok     bool
	}{
		"display needs description": {func(d *Definition) { d.Usage, d.InputSchema = []string{"display"}, input }, false},
		"display needs inputSchema": {func(d *Definition) { d.Usage, d.Description = []string{"display"}, "Show it." }, false},
		"form needs none":           {func(d *Definition) { d.Usage = []string{"form"} }, true},
		"display with contract": {func(d *Definition) {
			d.Usage, d.Description, d.InputSchema = []string{"display"}, "Show it.", input
		}, true},
		"description too long": {func(d *Definition) {
			d.Usage, d.Description, d.InputSchema = []string{"display"}, strings.Repeat("字", MaxDescriptionRunes+1), input
		}, false},
		"schema must compile": {func(d *Definition) {
			d.Usage, d.Description, d.InputSchema = []string{"display"}, "Show it.", json.RawMessage(`{"type":7}`)
		}, false},
		"schema size": {func(d *Definition) {
			d.Usage, d.Description = []string{"display"}, "Show it."
			d.InputSchema = json.RawMessage(`{"description":"` + strings.Repeat("a", MaxSchemaBytes) + `"}`)
		}, false},
		"schema requires display": {func(d *Definition) {
			d.Usage, d.InputSchema = []string{"form"}, input
		}, false},
	}
	for name, tc := range cases {
		d := base
		tc.mutate(&d)
		err := ValidateDefinitions(root, map[string]Definition{"main": d})
		if (err == nil) != tc.ok {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}

func TestRenderCatalogListsDisplayViewsDeterministically(t *testing.T) {
	mounts := []Mount{
		{ID: "zeta", Views: map[string]Definition{"card": {Title: "Card", Description: "Show\n a card.", Usage: []string{"display"}, InputSchema: json.RawMessage("{\n \"type\": \"object\"\n}")}}},
		{ID: "alpha", Views: map[string]Definition{
			"edit": {Description: "Edit.", Usage: []string{"form"}},
			"list": {Description: "List rows.", Usage: []string{"display", "form"}},
		}},
	}
	got, omitted := RenderCatalog(mounts)
	want := "- alpha/list — List rows.\n- zeta/card — Show a card."
	if got != want || omitted != 0 {
		t.Fatalf("catalog = %q omitted %d", got, omitted)
	}
}

func TestRenderCatalogDropsWholeEntriesPastLimit(t *testing.T) {
	views := map[string]Definition{}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "za", "zb", "zc", "zd", "ze", "zf", "zg", "zh", "zi", "zj"} {
		views[key] = Definition{Description: strings.Repeat("d", MaxDescriptionRunes), Usage: []string{"display"}}
	}
	got, omitted := RenderCatalog([]Mount{{ID: "big", Views: views}})
	if omitted == 0 || len(got) > MaxCatalogBytes || !strings.HasSuffix(got, strings.Repeat("d", MaxDescriptionRunes)) {
		t.Fatalf("len=%d omitted=%d", len(got), omitted)
	}
}

func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func TestDescribeDisclosesOneMountedDisplayView(t *testing.T) {
	mounts := []Mount{{ID: "crm", Views: map[string]Definition{
		"orders": {Title: "Orders", Description: "List\n orders.", Usage: []string{"display"}, InputSchema: json.RawMessage("{\n \"type\": \"object\"\n}"),
			Queries: map[string]Query{"list": {Description: "List.", Adapter: "native", Operation: "crm.list", ParamsSchema: json.RawMessage(`{"type": "object"}`)}}},
		"edit": {Description: "Edit.", Usage: []string{"form"}},
	}}}
	got, err := Describe(mounts, "crm/orders")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(got)
	want := `{"key":"crm/orders","title":"Orders","description":"List orders.","inputSchema":{"type":"object"},` +
		`"usage":["` + "```view key=crm/orders\\n{input matching inputSchema}\\n```" + `"]}`
	if string(data) != want {
		t.Fatalf("describe = %s", data)
	}
	// Queries stay private to the template; form views and other mounts are not found.
	for _, key := range []string{"crm/edit", "crm/missing", "other/orders", "orders", ""} {
		if _, err := Describe(mounts, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", key, err)
		}
	}
}
