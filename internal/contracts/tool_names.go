package contracts

import "strings"

const InvokeAgentsToolName = "agent_invoke"

// MCP tool mode gateway tools. The platform offers them instead of mounted MCP
// tools when tools.yml mcp.tool-mode is true; Agents cannot declare them.
const (
	MCPSearchToolName = "mcp_search"
	MCPCallToolName   = "mcp_call"
)

var MCPGatewayToolNames = []string{MCPSearchToolName, MCPCallToolName}

func IsMCPGatewayToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case MCPSearchToolName, MCPCallToolName:
		return true
	}
	return false
}

// ViewDescribeToolName discloses one mounted display view on demand. It is an
// ordinary tool, preset per mode in agent-settings.yml.
const ViewDescribeToolName = "view_describe"

const PlanAddTasksToolName = "plan_add_tasks"
const PlanGetTasksToolName = "plan_get_tasks"
const PlanUpdateTaskToolName = "plan_update_task"

var PlanTaskToolNames = []string{
	PlanAddTasksToolName,
	PlanGetTasksToolName,
	PlanUpdateTaskToolName,
}

const MaxInvokeAgentTasks = 5

func IsPlanTaskToolName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, toolName := range PlanTaskToolNames {
		if normalized == strings.ToLower(toolName) {
			return true
		}
	}
	return false
}
