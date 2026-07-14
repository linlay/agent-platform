package team

import (
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/api"
)

func testMembers() []MemberSpec {
	return []MemberSpec{{Key: "writer", Name: "Writer"}, {Key: "reviewer", Name: "Reviewer"}}
}

func testBaseToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key: ToolDelegate, Name: ToolDelegate,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"agentKey": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		Meta: map[string]any{"clientVisible": false, "explicitOnly": true, "internalOnly": true, "catalogVisible": false},
	}
}

func TestBuildToolDefinitionFreezesRosterAndKeepsEmbeddedMetadata(t *testing.T) {
	definition, err := BuildToolDefinition(testBaseToolDefinition(), testMembers())
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != ToolDelegate || definition.Meta["internalOnly"] != true || definition.Meta["catalogVisible"] != false {
		t.Fatalf("unexpected definition %#v", definition)
	}
	properties := definition.Parameters["properties"].(map[string]any)
	tasks := properties["tasks"].(map[string]any)
	if tasks["maxItems"] != 2 {
		t.Fatalf("tasks maxItems=%#v", tasks["maxItems"])
	}
	items := tasks["items"].(map[string]any)
	agentKey := items["properties"].(map[string]any)["agentKey"].(map[string]any)
	if !reflect.DeepEqual(agentKey["enum"], []string{"writer", "reviewer"}) {
		t.Fatalf("agent enum=%#v", agentKey["enum"])
	}
}

func TestParseDispatchNormalizesUnifiedDelegations(t *testing.T) {
	dispatch, err := ParseDispatch(ToolDelegate, map[string]any{
		"tasks": []any{
			map[string]any{"agentKey": " writer ", "taskName": " Draft ", "files": []any{" a.md ", "a.md", ""}},
			map[string]any{"agentKey": "reviewer", "task": " review "},
		},
	}, testMembers())
	if err != nil {
		t.Fatal(err)
	}
	want := Dispatch{Tasks: []TaskSpec{
		{AgentKey: "writer", TaskName: "Draft", Files: []string{"a.md"}},
		{AgentKey: "reviewer", Task: "review", Files: []string{}},
	}}
	if !reflect.DeepEqual(dispatch, want) {
		t.Fatalf("dispatch=%#v, want %#v", dispatch, want)
	}
}

func TestParseDispatchRejectsInvalidDelegations(t *testing.T) {
	tooManyFiles := make([]any, 11)
	for index := range tooManyFiles {
		tooManyFiles[index] = "file-" + string(rune('a'+index))
	}
	tests := []struct {
		name      string
		tool      string
		args      map[string]any
		members   []MemberSpec
		wantError error
	}{
		{name: "unknown tool", tool: "bash", args: map[string]any{}, members: testMembers(), wantError: ErrUnknownTool},
		{name: "empty tasks", tool: ToolDelegate, args: map[string]any{"tasks": []any{}}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "missing agent", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{}}}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "unknown member", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "missing"}}}, members: testMembers(), wantError: ErrUnknownMember},
		{name: "case mismatch", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "WRITER"}}}, members: testMembers(), wantError: ErrUnknownMember},
		{name: "duplicate member", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "writer"}, map[string]any{"agentKey": "writer"}}}, members: testMembers(), wantError: ErrDuplicateMember},
		{name: "over roster", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "writer"}, map[string]any{"agentKey": "reviewer"}, map[string]any{"agentKey": "writer"}}}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "no roster", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "writer"}}}, wantError: ErrNoAvailableMember},
		{name: "unknown field", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "writer", "extra": true}}}, members: testMembers(), wantError: ErrInvalidArguments},
		{name: "too many files", tool: ToolDelegate, args: map[string]any{"tasks": []any{map[string]any{"agentKey": "writer", "files": tooManyFiles}}}, members: testMembers(), wantError: ErrInvalidArguments},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDispatch(tc.tool, tc.args, tc.members)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error=%v, want %v", err, tc.wantError)
			}
		})
	}
}
