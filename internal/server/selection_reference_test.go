package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"context"
	"testing"
)

func TestQuerySelectionDoesNotAcquireFilePath(t *testing.T) {
	s := &Server{}
	refs, err := s.prepareQueryReferences(context.Background(), "chat", []api.Reference{{ID: "uuid", Type: "selection", Name: "Selected text", Path: "/fake", Text: "quote", Annotation: "revise"}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err = s.normalizeReferencePathsForAgent(refs, "chat", catalog.AgentDefinition{}, contracts.LocalPaths{})
	if err != nil {
		t.Fatal(err)
	}
	roundtrip := apiReferencesFromRuntime(runtimeReferencesFromAPI(refs))
	if len(roundtrip) != 1 || roundtrip[0].Path != "" || roundtrip[0].Text != "quote" || roundtrip[0].Annotation != "revise" || roundtrip[0].ID != "uuid" {
		t.Fatalf("%+v", roundtrip)
	}
}
