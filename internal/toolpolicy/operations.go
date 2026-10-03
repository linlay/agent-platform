package toolpolicy

import (
	"agent-platform/internal/connector"
	"strings"
)

// Operation contains scheduling policy only; handlers own validation and execution.
type Operation struct {
	Name          string
	ReadOnly      bool
	Barrier       bool
	AllowedStages []string
}

var operations = map[string]map[string]Operation{

	"run_env": {
		"list":    {ReadOnly: true, AllowedStages: []string{"all"}},
		"explain": {ReadOnly: true, AllowedStages: []string{"all"}},
		"set":     {Barrier: true, AllowedStages: []string{"main"}},
		"unset":   {Barrier: true, AllowedStages: []string{"main"}},
		"update":  {Barrier: true, AllowedStages: []string{"main"}},
	},
}

func OperationAware(tool string) bool {
	if owner, ok := connector.NativeToolConnector(tool); ok && owner == connector.PlatformControlConnectorID {
		return true
	}
	_, ok := operations[strings.ToLower(strings.TrimSpace(tool))]
	return ok
}
func LookupOperation(tool, name string) (Operation, bool) {
	if a, ok := connector.LookupControlAction(tool, name); ok {
		stage := "main"
		if a.ReadOnly && !strings.HasPrefix(tool, "desktop_") {
			stage = "all"
		}
		return Operation{Name: name, ReadOnly: a.ReadOnly, Barrier: !a.ReadOnly || strings.HasPrefix(tool, "desktop_"), AllowedStages: []string{stage}}, true
	}
	name = strings.ToLower(strings.TrimSpace(name))
	policy, ok := operations[strings.ToLower(strings.TrimSpace(tool))][name]
	policy.Name = name
	policy.AllowedStages = append([]string(nil), policy.AllowedStages...)
	return policy, ok
}
func InvocationDescriptor(tool string, args map[string]any) (Operation, bool) {
	name, _ := args["operation"].(string)
	if owner, ok := connector.NativeToolConnector(tool); ok && owner == connector.PlatformControlConnectorID {
		name, _ = args["action"].(string)
	}
	return LookupOperation(tool, name)
}
func (o Operation) AllowsExecutionPolicy(policy string) bool {
	stage := "main"
	if strings.EqualFold(strings.TrimSpace(policy), "read_only") {
		stage = "planning"
	}
	for _, allowed := range o.AllowedStages {
		if allowed == "all" || allowed == stage {
			return true
		}
	}
	return false
}
