package queryinput

import (
	"encoding/json"
	"fmt"
)

type QueryModelOptions struct {
	Key             string `json:"key,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
}

type Scene struct {
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type Reference struct {
	AnnotationIndex *int           `json:"annotationIndex,omitempty"`
	ID              string         `json:"id,omitempty"`
	Type            string         `json:"type,omitempty"`
	Text            string         `json:"text,omitempty"`
	Annotation      string         `json:"annotation,omitempty"`
	Name            string         `json:"name,omitempty"`
	Path            string         `json:"path,omitempty"`
	MimeType        string         `json:"mimeType,omitempty"`
	SizeBytes       *int64         `json:"sizeBytes,omitempty"`
	URL             string         `json:"url,omitempty"`
	SHA256          string         `json:"sha256,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

const ReferenceSandboxPathRemovedMessage = "reference sandboxPath has been removed; use path"

func (r *Reference) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["sandboxPath"]; ok {
		return fmt.Errorf(ReferenceSandboxPathRemovedMessage)
	}
	type referenceAlias Reference
	var decoded referenceAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = Reference(decoded)
	return nil
}
