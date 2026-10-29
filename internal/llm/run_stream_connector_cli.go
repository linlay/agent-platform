package llm

import (
	"fmt"
	"regexp"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	"agent-platform/internal/view"
)

// checkBashHITL routes a Bash call of a mounted connector command that a VIEW
// action declares intercepted to that view's form. Execution trust never
// suppresses the review, and a call the template cannot read back is refused
// rather than run unreviewed.
func (s *llmRunStream) checkBashHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return hitl.InterceptResult{}
	}
	mounts := s.session.ViewMounts
	if s.execCtx != nil && len(s.execCtx.Session.ViewMounts) > 0 {
		mounts = s.execCtx.Session.ViewMounts
	}
	reviewed := interceptedConnectors(mounts)
	if len(reviewed) == 0 {
		return hitl.InterceptResult{}
	}
	plan := s.rawBashAccessReview(invocation)
	var ids []string
	for _, id := range plan.ConnectorIDs {
		if _, ok := reviewed[id]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return hitl.InterceptResult{}
	}
	command := mapStringArg(invocation.args, "command")
	refused := func(reason string) hitl.InterceptResult {
		return hitl.InterceptResult{Intercepted: true, OriginalCommand: command, MatchedCommand: command, MatchedWhole: true, Conflict: reason}
	}
	call := plan.ConnectorCall
	if call == nil || len(ids) > 1 || call.ConnectorID != ids[0] {
		return refused(fmt.Sprintf("Connector %s reviews some of its commands in a view; run it as a single direct command without pipes, wrappers, redirects or variables.", strings.Join(ids, ", ")))
	}
	found := view.FindIntercept(reviewed[call.ConnectorID], call.Argv)
	if found == nil {
		return hitl.InterceptResult{}
	}
	if found.Err != nil {
		return refused(fmt.Sprintf("This %s command is reviewed in view %s/%s and must use only its declared arguments: %v", call.ConnectorID, call.ConnectorID, found.ViewKey, found.Err))
	}
	title := strings.TrimSpace(found.Action.Description)
	if title == "" {
		title = found.Name
	}
	return hitl.InterceptResult{
		Intercepted: true, OriginalCommand: command, MatchedCommand: command, MatchedWhole: true,
		Rule: hitl.FlatRule{
			Mode:    "form",
			View:    &view.Reference{ConnectorID: call.ConnectorID, Key: found.ViewKey},
			RuleKey: "view-intercept:" + call.ConnectorID + "/" + found.ViewKey + "/" + found.Name,
			Title:   title,
		},
		Intercept: &hitl.ViewIntercept{ConnectorID: call.ConnectorID, Prefix: call.Prefix, Action: *found},
	}
}

// interceptedConnectors keeps the frozen mounts that declare intercept actions.
func interceptedConnectors(mounts []view.Mount) map[string]map[string]view.Definition {
	var out map[string]map[string]view.Definition
	for _, mount := range mounts {
		for _, d := range mount.Views {
			intercepts := false
			for _, action := range d.Actions {
				intercepts = intercepts || action.Intercept
			}
			if intercepts {
				if out == nil {
					out = map[string]map[string]view.Definition{}
				}
				out[mount.ID] = mount.Views
				break
			}
		}
	}
	return out
}

// rebuildInterceptedCommand turns submitted form params back into one direct
// call through the action template; the params never reach a shell unquoted.
func rebuildInterceptedCommand(match hitl.InterceptResult, data map[string]any) (string, error) {
	if match.Intercept == nil {
		return "", fmt.Errorf("form is not bound to a reviewed command")
	}
	params, ok := data["params"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("data.params must be an object")
	}
	action := match.Intercept.Action.Action
	if err := view.ValidateParams(action, params); err != nil {
		return "", err
	}
	argv, err := view.CLIArgs(action.Args, params)
	if err != nil {
		return "", err
	}
	words := make([]string, 0, len(match.Intercept.Prefix)+len(argv))
	for _, word := range append(append([]string(nil), match.Intercept.Prefix...), argv...) {
		words = append(words, shellWord(word))
	}
	return strings.Join(words, " "), nil
}

// interceptFormData is what the view form starts from: the action and the
// params read back from the Agent's command.
func interceptFormData(match hitl.InterceptResult) map[string]any {
	if match.Intercept == nil {
		return nil
	}
	return map[string]any{"action": match.Intercept.Action.Name, "params": contracts.CloneMap(match.Intercept.Action.Params)}
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

func shellWord(value string) string {
	if plainShellWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
