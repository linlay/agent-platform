package team

import (
	"errors"
	"reflect"
	"testing"
)

func testMembers() []MemberSpec {
	return []MemberSpec{{Key: "writer", Name: "Writer"}, {Key: "reviewer", Name: "Reviewer"}}
}

func TestHiddenToolDefinitionsAreSessionLocalAndHidden(t *testing.T) {
	defs := HiddenToolDefinitions(testMembers(), 2)
	if len(defs) != 2 || defs[0].Name != ToolDelegate || defs[1].Name != ToolInvoke {
		t.Fatalf("unexpected definitions %#v", defs)
	}
	for _, def := range defs {
		if def.Meta["clientVisible"] != false || def.Meta["explicitOnly"] != true || def.Meta["internalOnly"] != true || def.Meta["catalogVisible"] != false {
			t.Fatalf("tool %s is not hidden: %#v", def.Name, def.Meta)
		}
	}
	delegateProperties := defs[0].Parameters["properties"].(map[string]any)
	memberSchema := delegateProperties["memberKey"].(map[string]any)
	if !reflect.DeepEqual(memberSchema["enum"], []string{"writer", "reviewer"}) {
		t.Fatalf("member enum=%#v", memberSchema["enum"])
	}
	invokeProperties := defs[1].Parameters["properties"].(map[string]any)
	tasksSchema := invokeProperties["tasks"].(map[string]any)
	if tasksSchema["maxItems"] != 2 {
		t.Fatalf("invoke maxItems=%#v", tasksSchema["maxItems"])
	}
}

func TestParseDispatchDirectCanonicalizesMember(t *testing.T) {
	dispatch, err := ParseDispatch(ToolDelegate, map[string]any{
		"mode":      " DIRECT ",
		"memberKey": " WRITER ",
	}, testMembers(), 5)
	if err != nil {
		t.Fatal(err)
	}
	want := Dispatch{Kind: DispatchKindDirect, DelegateMode: DelegateModeDirect, Tasks: []TaskSpec{{MemberKey: "writer"}}}
	if !reflect.DeepEqual(dispatch, want) {
		t.Fatalf("dispatch=%#v, want %#v", dispatch, want)
	}
}

func TestParseDispatchFanoutTargetsEveryUniqueMember(t *testing.T) {
	members := append(testMembers(), MemberSpec{Key: " WRITER "}, MemberSpec{})
	dispatch, err := ParseDispatch(ToolDelegate, map[string]any{"mode": DelegateModeFanout}, members, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Kind != DispatchKindFanout || dispatch.DelegateMode != DelegateModeFanout {
		t.Fatalf("unexpected fanout %#v", dispatch)
	}
	if !reflect.DeepEqual(dispatch.Tasks, []TaskSpec{{MemberKey: "writer"}, {MemberKey: "reviewer"}}) {
		t.Fatalf("fanout tasks=%#v", dispatch.Tasks)
	}
}

func TestParseDispatchInvokeValidatesAndNormalizesBatch(t *testing.T) {
	dispatch, err := ParseDispatch(ToolInvoke, map[string]any{
		"tasks": []any{
			map[string]any{"memberKey": "writer", "task": " draft ", "taskName": " Draft ", "files": []any{" a.md ", "a.md", ""}},
			map[string]any{"memberKey": "REVIEWER", "task": "review"},
		},
	}, testMembers(), 2)
	if err != nil {
		t.Fatal(err)
	}
	want := Dispatch{Kind: DispatchKindInvoke, Tasks: []TaskSpec{
		{MemberKey: "writer", Task: "draft", TaskName: "Draft", Files: []string{"a.md"}},
		{MemberKey: "reviewer", Task: "review", Files: []string{}},
	}}
	if !reflect.DeepEqual(dispatch, want) {
		t.Fatalf("dispatch=%#v, want %#v", dispatch, want)
	}
}

func TestParseDispatchRejectsInvalidRoutes(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		args      map[string]any
		members   []MemberSpec
		limit     int
		wantError error
	}{
		{name: "unknown tool", tool: "bash", args: map[string]any{}, members: testMembers(), wantError: ErrUnknownTool},
		{name: "direct missing member", tool: ToolDelegate, args: map[string]any{"mode": "direct"}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "unknown member", tool: ToolDelegate, args: map[string]any{"mode": "direct", "memberKey": "missing"}, members: testMembers(), wantError: ErrUnknownMember},
		{name: "fanout with member", tool: ToolDelegate, args: map[string]any{"mode": "fanout", "memberKey": "writer"}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "fanout empty", tool: ToolDelegate, args: map[string]any{"mode": "fanout"}, wantError: ErrNoAvailableMember},
		{name: "unknown field", tool: ToolDelegate, args: map[string]any{"mode": "fanout", "extra": true}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "invoke over limit", tool: ToolInvoke, args: map[string]any{"tasks": []any{
			map[string]any{"memberKey": "writer", "task": "one"},
			map[string]any{"memberKey": "reviewer", "task": "two"},
		}}, members: testMembers(), limit: 1, wantError: ErrInvalidArguments},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDispatch(tc.tool, tc.args, tc.members, tc.limit)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error=%v, want %v", err, tc.wantError)
			}
		})
	}
}
