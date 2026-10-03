package platformcontrol

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
	"errors"
	"strings"
)

func controlActionNames(tool string) []string {
	var names []string
	for _, a := range connector.ControlActions() {
		if a.Tool == tool {
			names = append(names, a.Action)
		}
	}
	return names
}
func controlInputFailure(code string, err error, stage string) contracts.ToolExecutionResult {
	r := controlFail(code, sanitizeDiagnostic(err.Error()), stage)
	var input *toolinput.Error
	if errors.As(err, &input) {
		for k, v := range input.Details() {
			r.Structured[k] = v
		}
		r.Structured["message"] = input.Error()
		r.Output = contracts.CompactToolModelOutput(r.Structured, "")
	}
	return r
}
func validateControlEnums(tool, action string, p map[string]any) error {
	enums := map[string][]string{}
	if tool == "catalog_query" && action == "defaults" {
		enums["type"] = []string{"general", "coder", "kbase"}
	}
	if tool == "catalog_query" && action == "list" {
		enums["status"] = []string{"", "all", "valid", "invalid"}
	}
	// Resource capabilities stay in their existing service; do not broaden write permissions here.
	if tool == "catalog_query" && (action == "list" || action == "get") {
		enums["resourceType"] = []string{"agent", "team", "skill", "connector", "model", "tool", "provider", "mcp"}
	}
	if tool == "catalog_manage" || (tool == "catalog_query" && action == "validate") {
		enums["resourceType"] = []string{"agent", "team", "skill", "connector"}
	}
	if tool == "chat_query" {
		if action == "list" || action == "search" {
			enums["scope"] = []string{"", "agent", "instance"}
		}
		if action == "read" {
			enums["view"] = []string{"summary", "messages"}
		}
	}
	if tool == "chat_manage" && action == "export" {
		enums["format"] = []string{"markdown", "snapshot"}
	}
	if tool == "platform_inspect" && action == "securityExplain" {
		enums["access"] = []string{"", "read", "write"}
	}
	for _, field := range toolinput.Keys(enums) {
		v, exists := p[field]
		if exists || strings.HasSuffix(argumentFields[tool+"."+action][field], "!") {
			if err := toolinput.Choice("args."+field, v, exists, enums[field]); err != nil {
				return err
			}
		}
	}
	return nil
}
