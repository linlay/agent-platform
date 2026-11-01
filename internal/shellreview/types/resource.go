package types

// ResourceState distinguishes uncertainty from a proven violation. A failed
// probe never removes path facts or a hard violation discovered elsewhere.
type ResourceState string

const (
	Known         ResourceState = "known"
	Unresolved    ResourceState = "unresolved"
	HardViolation ResourceState = "hard_violation"
)

type ResourceResult struct {
	State      ResourceState
	NullDevice bool
	Reason     string
}
