package view

import (
	"encoding/json"
	"strings"
	"testing"
)

func queryDefinition(t *testing.T, query string) Definition {
	t.Helper()
	var d Definition
	raw := `{"description":"Orders.","renderer":"html","usage":["display"],"entry":"views/orders.html","queries":{"list":` + query + `}}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return d
}

func TestQueryDeclarationsShareOneShapeAcrossAdapters(t *testing.T) {
	for _, query := range []string{
		`{"adapter":"native","operation":"automation.list"}`,
		`{"adapter":"cli","args":["orders","list",{"param":"id"},{"opt":"--status","param":"status"},{"flag":"-a","param":"all"}]}`,
		`{"adapter":"mcp","component":"crmapi","toolName":"get_order","arguments":{"fixed":1},"paramsSchema":{"type":"object","properties":{"id":{"type":"string"}}}}`,
	} {
		if err := validateQueries("orders", queryDefinition(t, query)); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
}

func TestQueryDeclarationsRejectMixedOrUnsafeShapes(t *testing.T) {
	for query, want := range map[string]string{
		`{"adapter":"shell","args":["x"]}`:                                  "adapter must be",
		`{"adapter":"native","operation":"automation.list","args":["x"]}`:   "only operation",
		`{"adapter":"cli","args":[]}`:                                       "only args",
		`{"adapter":"cli","args":["x"],"toolName":"t"}`:                     "only args",
		`{"adapter":"cli","args":[""]}`:                                     "literal",
		`{"adapter":"cli","args":[{"opt":"--a","flag":"--b","param":"p"}]}`: "both opt and flag",
		`{"adapter":"cli","args":[{"opt":"status","param":"p"}]}`:           "must look like",
		`{"adapter":"cli","args":[{"param":"p q"}]}`:                        "arg param",
		`{"adapter":"mcp","component":"crmapi"}`:                            "component and toolName",
		`{"adapter":"mcp","component":"c","toolName":"t","arguments":{"id":1},"paramsSchema":{"type":"object","properties":{"id":{}}}}`: "override a fixed",
	} {
		err := validateQueries("orders", queryDefinition(t, query))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v, want %q", query, err, want)
		}
	}
	var d Definition
	for _, bad := range []string{`{"queries":{"list":{"adapter":"cli","args":[{"opt":"--a"}]}}}`, `{"queries":{"list":{"adapter":"cli","args":[{"param":"p","shell":true}]}}}`, `{"queries":{"list":{"adapter":"cli","args":[3]}}}`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Fatalf("%s decoded", bad)
		}
	}
}

// Snapshot hashes cover the marshaled queries, so args must round-trip.
func TestQueryArgsRoundTripForSnapshots(t *testing.T) {
	raw := `["orders",{"param":"id"},{"param":"status","opt":"--status"},{"param":"all","flag":"--all"}]`
	var args []QueryArg
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(args)
	if err != nil || string(data) != raw {
		t.Fatalf("%s %v", data, err)
	}
}
