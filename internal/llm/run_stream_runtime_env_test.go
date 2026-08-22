package llm

import (
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
)

func TestKnownRuntimeVariablesMergesDynamicIntoEmptyStaticEnvironment(t *testing.T) {
	scope := runenv.NewScope(runenv.Limits{})
	defer scope.Destroy()

	if _, err := scope.Mutate(runenv.MutationRequest{
		Operation: runenv.OperationSet,
		Name:      "DOCUMENT_ID",
		Value:     "dynamic-value",
	}); err != nil {
		t.Fatal(err)
	}

	stream := &llmRunStream{execCtx: &contracts.ExecutionContext{RunEnvironment: scope}}
	variables := stream.knownRuntimeVariables()
	if got := variables["DOCUMENT_ID"]; got != "dynamic-value" {
		t.Fatalf("DOCUMENT_ID = %q, want dynamic-value", got)
	}

	stream.execCtx.StaticRuntimeEnv = map[string]string{"DOCUMENT_ID": "static-value", "STATIC_ONLY": "keep"}
	variables = stream.knownRuntimeVariables()
	if got := variables["DOCUMENT_ID"]; got != "dynamic-value" {
		t.Fatalf("dynamic DOCUMENT_ID = %q, want dynamic-value", got)
	}
	if got := variables["STATIC_ONLY"]; got != "keep" {
		t.Fatalf("STATIC_ONLY = %q, want keep", got)
	}
}
