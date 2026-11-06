//go:build !darwin && !windows

package processgroup

// Other Unix hosts retain the existing process-group boundary. Windows owns
// descendants through its Job Object and does not use the Unix guardian.
type descendantTracker struct{}

func newDescendantTracker(int) (*descendantTracker, error) { return &descendantTracker{}, nil }
func (*descendantTracker) capture() error                  { return nil }
func (*descendantTracker) terminate() (bool, error)        { return true, nil }
