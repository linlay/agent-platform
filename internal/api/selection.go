package api

import (
	"fmt"
	"strings"
)

// NormalizeSelectionReference strips file capabilities from quoted text.
func NormalizeSelectionReference(ref Reference) (Reference, error) {
	if ref.AnnotationIndex != nil && (*ref.AnnotationIndex <= 0 || int64(*ref.AnnotationIndex) > 9007199254740991) {
		return Reference{}, fmt.Errorf("selection annotationIndex must be a positive safe integer")
	}
	text := ref.Text
	if strings.TrimSpace(text) == "" {
		return Reference{}, fmt.Errorf("selection requires non-empty text")
	}
	var annotationIndex *int
	if ref.AnnotationIndex != nil {
		value := *ref.AnnotationIndex
		annotationIndex = &value
	}
	return Reference{AnnotationIndex: annotationIndex, ID: ref.ID, Type: "selection", Text: text, Annotation: ref.Annotation}, nil
}
