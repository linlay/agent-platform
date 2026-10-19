package orchestration

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestRouteChildUsageSnapshotNamespacesToolUsage(t *testing.T) {
	tool := &stream.UsageSnapshotTool{ToolID: "tool_1", ModelKey: "image-model"}
	routed, ok := RouteChildStreamInput("run_parent", "task_a", stream.InputUsageSnapshot{Tool: tool}).(stream.InputUsageSnapshot)
	if !ok || routed.TaskID != "task_a" || routed.Tool == nil || routed.Tool.ToolID != "task_a:tool_1" {
		t.Fatalf("tool usage must follow the child's namespaced tool ID, got %#v", routed)
	}
	if tool.ToolID != "tool_1" {
		t.Fatalf("routing must not mutate the child's input, got %#v", tool)
	}
}
