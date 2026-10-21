package llm

import (
	"testing"

	. "agent-platform/internal/contracts"
)

func TestMergeConcurrentExecutionContextAccumulatesFilePages(t *testing.T) {
	base := ReadFileSnapshot{SHA256: "v1", TotalLines: 30, Ranges: []LineRange{{Start: 1, End: 10}}, ReadAtUnixMs: 1}
	stream := &llmRunStream{execCtx: &ExecutionContext{ReadFileState: map[string]ReadFileSnapshot{"/f": base}}}

	// Two parallel calls each start from a clone and read a different page.
	pageTwo := cloneReadFileState(stream.execCtx.ReadFileState)
	pageThree := cloneReadFileState(stream.execCtx.ReadFileState)
	pageTwo["/f"] = MergeReadFileSnapshot(pageTwo["/f"], ReadFileSnapshot{SHA256: "v1", TotalLines: 30, Ranges: []LineRange{{Start: 11, End: 20}}, ReadAtUnixMs: 2})
	pageThree["/f"] = MergeReadFileSnapshot(pageThree["/f"], ReadFileSnapshot{SHA256: "v1", TotalLines: 30, Ranges: []LineRange{{Start: 21, End: 30}}, ReadAtUnixMs: 3})
	stream.mergeConcurrentExecutionContext(&ExecutionContext{ReadFileState: pageThree})
	stream.mergeConcurrentExecutionContext(&ExecutionContext{ReadFileState: pageTwo})

	if merged := stream.execCtx.ReadFileState["/f"]; !merged.Complete() || merged.ReadAtUnixMs != 3 {
		t.Fatalf("expected all pages merged, got %#v", merged)
	}
	if base.Ranges[0].End != 10 {
		t.Fatal("clone shared range storage with the parent state")
	}
}
