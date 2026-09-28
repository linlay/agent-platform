package contracts

import "agent-platform/internal/modelcontent"

type ReasoningPart = modelcontent.ReasoningPart

func EncryptedReasoningParts(value any) []ReasoningPart {
	return modelcontent.EncryptedReasoningParts(value)
}
func ReasoningPartsValue(text string, parts []ReasoningPart) any {
	return modelcontent.ReasoningPartsValue(text, parts)
}
