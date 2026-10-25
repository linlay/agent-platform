package contracts

// ToolActivityArgs returns the argument names a tool definition lists in
// activityArgs, in order. A running tool shows the first non-empty one.
func ToolActivityArgs(meta map[string]any) []string {
	switch values := meta["activityArgs"].(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if name, ok := value.(string); ok && name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}
