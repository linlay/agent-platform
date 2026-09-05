package accesspolicy

func (p PathPlan) RequiresApproval() bool {
	return p.Decision == DecisionRequiresApproval
}
