package toolpolicy

import "strings"

// Operation contains scheduling policy only; handlers own validation and execution.
type Operation struct {
	Name          string
	ReadOnly      bool
	Barrier       bool
	AllowedStages []string
}

var operations = map[string]map[string]Operation{
	"platform_control": {
		"capabilities.list":    {ReadOnly: true, AllowedStages: []string{"all"}},
		"catalog.defaults.get": {ReadOnly: true, AllowedStages: []string{"all"}},
		"catalog.validate":     {ReadOnly: true, AllowedStages: []string{"all"}},
		"chat.set_pinned":      {Barrier: true, AllowedStages: []string{"main"}},
		"runtime.status":       {ReadOnly: true, AllowedStages: []string{"all"}},
		"security.explain":     {ReadOnly: true, AllowedStages: []string{"all"}},
	},
	"run_env": {
		"list":    {ReadOnly: true, AllowedStages: []string{"all"}},
		"explain": {ReadOnly: true, AllowedStages: []string{"all"}},
		"set":     {Barrier: true, AllowedStages: []string{"main"}},
		"unset":   {Barrier: true, AllowedStages: []string{"main"}},
		"update":  {Barrier: true, AllowedStages: []string{"main"}},
	},
}

func OperationAware(tool string) bool {
	_, ok := operations[strings.ToLower(strings.TrimSpace(tool))]
	return ok
}
func LookupOperation(tool, name string) (Operation, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	policy, ok := operations[strings.ToLower(strings.TrimSpace(tool))][name]
	policy.Name = name
	policy.AllowedStages = append([]string(nil), policy.AllowedStages...)
	return policy, ok
}
func InvocationDescriptor(tool string, args map[string]any) (Operation, bool) {
	name, _ := args["operation"].(string)
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
