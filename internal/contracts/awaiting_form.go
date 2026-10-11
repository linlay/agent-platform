package contracts

// Form payloads are JSON trees. Copy nested maps/slices so an emitted event or
// a caller inspecting a waiter cannot alter its validation snapshot.
func cloneAwaitingForm(form map[string]any) map[string]any {
	if form == nil {
		return nil
	}
	var clone func(any) any
	clone = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			out := make(map[string]any, len(v))
			for k, item := range v {
				out[k] = clone(item)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = clone(item)
			}
			return out
		case []string:
			return append([]string(nil), v...)
		default:
			return value
		}
	}
	return clone(form).(map[string]any)
}
