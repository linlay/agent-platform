package server

// Push notifications carry the business-specific instant (updatedAt here)
// rather than the stream envelope timestamp.
func catalogUpdatedPushPayload(reason string, updatedAt int64) map[string]any {
	return map[string]any{
		"reason":    reason,
		"updatedAt": updatedAt,
	}
}
