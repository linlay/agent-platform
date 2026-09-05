package bashast

func ParseWithEmbeddedDetection(command string) (ParseResult, []EmbeddedScript) {
	return ParseWithEmbeddedDetectionAndKnownVariables(command, nil)
}
