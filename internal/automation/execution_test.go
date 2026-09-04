package automation

// synchronousExecutionRecorder keeps focused dispatcher/orchestrator tests
// deterministic without making the SQLite store a production recorder.
type synchronousExecutionRecorder struct {
	store *ExecutionStore
}

func (r synchronousExecutionRecorder) Submit(item Execution) {
	if r.store != nil {
		_ = r.store.Upsert(item)
	}
}
