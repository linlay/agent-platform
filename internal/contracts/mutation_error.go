package contracts

// MutationError distinguishes rejection before side effects from uncertain or committed writes.
type MutationError struct {
	State string
	Err   error
}

func (e *MutationError) Error() string { return e.Err.Error() }
func (e *MutationError) Unwrap() error { return e.Err }
