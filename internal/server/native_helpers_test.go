package server

import "agent-platform/internal/runtime/runexec"

var handleCompactCheckpointPersistenceFailure = runexec.HandleCompactCheckpointPersistenceFailure
var persistRunCompletionWithReason = runexec.PersistRunCompletionWithReason

var StartRunExecutor = runexec.StartNative
var runExecutor = runexec.ExecuteNative
