package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type maintenanceFake struct {
	mu               sync.Mutex
	collection       map[string]any
	calls            []string
	started, release chan struct{}
	once             sync.Once
}

func (f *maintenanceFake) Run(ctx context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()
	if args[0] == "capabilities" {
		return []byte(`{"schemaVersion":1,"type":"kbx.capabilities","maintenance":{"schemaVersion":1,"structuredErrors":true,"registerWithoutIndex":true,"jsonCommands":["collection.add","collection.list","collection.show","status","update","embed"],"includes":{"expression":"globset","multiple":"braceAlternation","caseSensitive":true},"ignore":{"multiple":true,"caseSensitive":true,"builtinsOverrideIncludes":true},"pathUpdates":{"schemaVersion":1,"singleCollection":true,"filesOnly":true}}}`), nil
	}
	op := args[0]
	var data any
	if op == "collection" {
		op += "." + args[1]
		switch args[1] {
		case "list":
			items := []any{}
			if f.collection != nil {
				items = append(items, f.collection)
			}
			data = map[string]any{"collections": items}
		case "add":
			f.collection = map[string]any{"name": args[4], "path": args[2]}
		case "set-pattern":
			f.collection["pattern"] = args[3]
		case "set-ignore":
			f.collection["ignore"] = args[3:]
		case "set-chunking":
			f.collection["chunking"] = map[string]any{"strategy": "window", "max_chars": 3600, "overlap_chars": 540}
		case "show":
			data = f.collection
		}
	}
	if op == "update" && f.started != nil {
		f.once.Do(func() {
			close(f.started)
			select {
			case <-f.release:
			case <-ctx.Done():
			}
		})
	}
	r := map[string]any{"schemaVersion": 1, "type": "kbx.maintenance.response", "operation": op, "status": "complete", "exitCode": 0, "data": data, "index": map[string]any{"selected": map[string]any{"documents": 1, "fullText": map[string]any{"ready": true}, "vector": map[string]any{"state": "absent"}}}}
	return json.Marshal(r)
}
func TestMaintenancePartialAndNonzeroJSONAreNotSuccess(t *testing.T) {
	for _, status := range []string{"partial", "failed"} {
		t.Run(status, func(t *testing.T) {
			m, l := newTestManager(t)
			m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"schemaVersion":1,"type":"kbx.maintenance.response","operation":"update","status":%q,"exitCode":1,"error":{"code":"SOURCE_NOT_FOUND","message":"missing source"}}`, status)), fmt.Errorf("process exited 1")
			})
			r, e := m.maintenance(context.Background(), l, []byte("{}"), "update", "update")
			if e == nil || r.Error == nil || r.Error.Code != "SOURCE_NOT_FOUND" {
				t.Fatalf("lost JSON failure: %+v %v", r, e)
			}
		})
	}
}
func TestMaintenanceGlobsFailClosed(t *testing.T) {
	for _, p := range []string{"**/*.md", "docs/**/*.txt", "private/**", "file,one.md"} {
		if _, e := maintenancePattern(p); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []string{"*.md", "docs/*.md", "a?b.md", "**/private*.txt"} {
		if _, e := maintenancePattern(p); e == nil {
			t.Fatal("unsafe glob accepted", p)
		}
	}
}
