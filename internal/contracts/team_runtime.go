package contracts

// TeamRuntimeContext is the immutable TEAM-mode input copied from the
// run-scoped catalog snapshot. It intentionally contains no catalog types.
type TeamRuntimeContext struct {
	RuntimeMode             string
	MaxParallel             int
	Members                 []TeamMember
	RosterFingerprint       string
	ToolSchemaFingerprint   string
	OrchestratorFingerprint string
}

type TeamMember struct {
	Key         string
	Name        string
	Role        string
	Description string
}
