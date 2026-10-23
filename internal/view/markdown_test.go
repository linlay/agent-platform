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
				Query string          `json:"query"`
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
				if got.Index != want.Index || key != want.Key || got.Query != want.Query || ErrorCode(got.Err) != want.Error {
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

func TestValidateDataUsesDataSchema(t *testing.T) {
	d := Definition{DataSchema: json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"type":"array"}}}`)}
	if err := ValidateData(d, json.RawMessage(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateData(d, json.RawMessage(`{"items":"no"}`)); !errors.Is(err, ErrDataInvalid) {
		t.Fatalf("schema violation: %v", err)
	}
	if err := ValidateData(Definition{}, json.RawMessage(`[1]`)); err != nil {
		t.Fatalf("no schema: %v", err)
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
	cases := map[string]struct {
		mutate func(*Definition)
		ok     bool
	}{
		"display needs description": {func(d *Definition) { d.Usage = []string{"display"} }, false},
		"form needs none":           {func(d *Definition) { d.Usage = []string{"form"} }, true},
		"display with description":  {func(d *Definition) { d.Usage, d.Description = []string{"display"}, "Show it." }, true},
		"description too long": {func(d *Definition) {
			d.Usage, d.Description = []string{"display"}, strings.Repeat("字", MaxDescriptionRunes+1)
		}, false},
		"schema must compile": {func(d *Definition) {
			d.Usage, d.Description, d.DataSchema = []string{"display"}, "Show it.", json.RawMessage(`{"type":7}`)
		}, false},
		"schema size": {func(d *Definition) {
			d.Usage, d.Description = []string{"display"}, "Show it."
			d.DataSchema = json.RawMessage(`{"description":"` + strings.Repeat("a", MaxDataSchemaBytes) + `"}`)
		}, false},
		"schema requires display": {func(d *Definition) {
			d.Usage, d.DataSchema = []string{"form"}, json.RawMessage(`{"type":"object"}`)
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
		{ID: "zeta", Views: map[string]Definition{"card": {Title: "Card", Description: "Show\n a card.", Usage: []string{"display"}, DataSchema: json.RawMessage("{\n \"type\": \"object\"\n}")}}},
		{ID: "alpha", Views: map[string]Definition{
			"edit": {Description: "Edit.", Usage: []string{"form"}},
			"list": {Description: "List rows.", Usage: []string{"display", "form"}},
		}},
	}
	got, omitted := RenderCatalog(mounts)
	want := "key: alpha/list\ndescription: List rows.\n\nkey: zeta/card\ntitle: Card\ndescription: Show a card.\ndata: {\"type\":\"object\"}"
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
