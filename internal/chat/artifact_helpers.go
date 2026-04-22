package chat

func artifactItemsFromValue(raw any) []ArtifactItemState {
	items := toMapSlice(raw)
	if len(items) == 0 {
		return nil
	}

	result := make([]ArtifactItemState, 0, len(items))
	for _, item := range items {
		if artifact, ok := artifactItemFromMap(item, ""); ok {
			result = append(result, artifact)
		}
	}
	return result
}

func artifactItemsFromEventPayload(payload map[string]any) []ArtifactItemState {
	if len(payload) == 0 {
		return nil
	}
	if items := artifactItemsFromValue(payload["artifacts"]); len(items) > 0 {
		return items
	}
	item, _ := payload["artifact"].(map[string]any)
	if len(item) == 0 {
		return nil
	}
	if artifact, ok := artifactItemFromMap(item, stringValue(payload["artifactId"])); ok {
		return []ArtifactItemState{artifact}
	}
	return nil
}

func artifactItemFromMap(item map[string]any, fallbackID string) (ArtifactItemState, bool) {
	if len(item) == 0 {
		return ArtifactItemState{}, false
	}
	artifactID := stringValue(item["artifactId"])
	if artifactID == "" {
		artifactID = fallbackID
	}
	artifact := ArtifactItemState{
		ArtifactID: artifactID,
		Type:       stringValue(item["type"]),
		Name:       stringValue(item["name"]),
		MimeType:   stringValue(item["mimeType"]),
		SizeBytes:  int64FromAny(item["sizeBytes"]),
		URL:        stringValue(item["url"]),
		SHA256:     stringValue(item["sha256"]),
	}
	if artifact.ArtifactID == "" && artifact.Name == "" && artifact.URL == "" {
		return ArtifactItemState{}, false
	}
	return artifact, true
}
