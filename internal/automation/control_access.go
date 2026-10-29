package automation

import (
	"fmt"

	"agent-platform/internal/contracts"
)

func validateControlAccess(plan *ControlPlan, level string) error {
	if plan.Action == "delete" || plan.Action == "setEnabled" && !plan.Definition.Enabled {
		return nil
	}
	if contracts.AccessLevelRank(plan.Definition.Query.AccessLevel) > contracts.AccessLevelRank(level) {
		return &contracts.RunToolError{Code: "automation_access_level_exceeded", Message: "automation execution level exceeds the calling Run current level"}
	}
	return nil
}

func (s *Service) validateControlCaller(plan *ControlPlan, key, level string) error {
	// prepareControl already matched this invocation's request digest. Replaying
	// its completed result creates no new authority or schedule mutation.
	if receipt, err := s.controlReceipt(key); err == nil && receipt != nil && receipt.State == "completed" {
		return nil
	}
	return validateControlAccess(plan, level)
}

// Agent operations lock caller permission before the management transaction.
// HTTP management has no Agent caller and uses the ordinary service methods.
func (s *Service) PrepareControlForRun(action string, args map[string]any, key string, caller *contracts.RunControl) (*ControlPlan, error) {
	if caller == nil {
		return nil, fmt.Errorf("run_context_required: live permission control required")
	}
	var plan *ControlPlan
	err := caller.AcceptAtAccessLevel(func(level string, _ int64) error {
		var err error
		plan, err = s.PrepareControl(action, args, key)
		if err != nil {
			return err
		}
		return s.validateControlCaller(plan, key, level)
	})
	return plan, err
}

func (s *Service) ExecuteControlForRun(action string, args map[string]any, key string, caller *contracts.RunControl, authorize func(string) bool) (any, error) {
	if caller == nil {
		return nil, fmt.Errorf("run_context_required: live permission control required")
	}
	var result any
	err := caller.AcceptAtAccessLevel(func(level string, _ int64) error {
		var err error
		result, err = s.executeControl(action, args, key, authorize, &level)
		return err
	})
	return result, err
}
