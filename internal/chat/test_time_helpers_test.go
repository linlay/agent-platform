package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

// testEpochMillis keeps ordinary fixtures on the public wire contract while
// retaining small, readable offsets for ordering assertions.
func testEpochMillis(offset int64) int64 {
	return 1_700_000_000_000 + offset
}

// completeRunForTest is an explicit fixture builder for the new lifecycle
// invariant: production completion never creates a missing start row.
func completeRunForTest(store *FileStore, completion RunCompletion) error {
	if completion.UpdatedAtMillis < timecontract.MinEpochMillis {
		completion.UpdatedAtMillis = testEpochMillis(completion.UpdatedAtMillis)
	}
	if completion.StartedAtMillis < timecontract.MinEpochMillis {
		completion.StartedAtMillis = completion.UpdatedAtMillis - 1
	}
	if startedAt, ok, err := testRecordedRunStart(store, completion.RunID); err != nil {
		return err
	} else if ok {
		completion.StartedAtMillis = startedAt
	} else if err := store.OnRunStarted(RunStart{
		ChatID:          completion.ChatID,
		RunID:           completion.RunID,
		AgentKey:        completion.AgentKey,
		TeamID:          completion.TeamID,
		InitialMessage:  completion.InitialMessage,
		StartedAtMillis: completion.StartedAtMillis,
	}); err != nil {
		return err
	}
	return store.OnRunCompleted(completion)
}

// The older behaviour-focused tests used short, human-readable clock offsets
// and occasionally omitted timestamps that the production writer now
// requires. These builders are deliberately test-only: they construct valid
// current-format records before handing them to the same strict public write
// boundary. Contract tests call the production methods directly.
func appendQueryLineForTest(store *FileStore, chatID string, line QueryLine) error {
	line.UpdatedAt = testRequiredEpochMillis(line.UpdatedAt)
	for _, message := range line.Messages {
		normalizeTestPlatformTimestamp(message, "ts", line.UpdatedAt)
	}
	if err := ensureRunStartedForTest(store, chatID, line.RunID, line.UpdatedAt); err != nil {
		return err
	}
	return store.AppendQueryLine(chatID, line)
}

func appendStepLineForTest(store *FileStore, chatID string, line StepLine) error {
	line.UpdatedAt = testRequiredEpochMillis(line.UpdatedAt)
	for index := range line.Messages {
		if line.Messages[index].Ts != nil {
			value := testRequiredEpochMillis(*line.Messages[index].Ts)
			line.Messages[index].Ts = &value
		} else {
			value := line.UpdatedAt
			line.Messages[index].Ts = &value
		}
	}
	for _, awaiting := range line.Awaiting {
		normalizeTestPlatformTimestamp(awaiting, "timestamp", line.UpdatedAt)
	}
	if line.Sources != nil {
		for _, item := range line.Sources.Items {
			if _, exists := item["timestamp"]; exists {
				item["timestamp"] = normalizeTestEpochValue(item["timestamp"])
			}
		}
	}
	if err := ensureRunStartedForTest(store, chatID, line.RunID, line.UpdatedAt); err != nil {
		return err
	}
	return store.AppendStepLine(chatID, line)
}

func appendEventLineForTest(store *FileStore, chatID string, line EventLine) error {
	line.UpdatedAt = testRequiredEpochMillis(line.UpdatedAt)
	if line.Event == nil {
		line.Event = map[string]any{}
	}
	normalizeTestPlatformTimestamp(line.Event, "timestamp", line.UpdatedAt)
	if err := ensureRunStartedForTest(store, chatID, line.RunID, line.UpdatedAt); err != nil {
		return err
	}
	return store.AppendEventLine(chatID, line)
}

func appendSubmitLineForTest(store *FileStore, chatID string, line SubmitLine) error {
	line.UpdatedAt = testRequiredEpochMillis(line.UpdatedAt)
	for _, payload := range []map[string]any{line.Submit, line.Answer} {
		if len(payload) == 0 {
			continue
		}
		normalizeTestPlatformTimestamp(payload, "timestamp", line.UpdatedAt)
	}
	if err := ensureRunStartedForTest(store, chatID, line.RunID, line.UpdatedAt); err != nil {
		return err
	}
	return store.AppendSubmitLine(chatID, line)
}

func onEventForTest(writer *StepWriter, event stream.EventData) {
	if writer == nil {
		return
	}
	event.Timestamp = testRequiredEpochMillis(event.Timestamp)
	if event.Type == "request.query" {
		normalizeTestMessageSlice(event.Payload["messages"], event.Timestamp)
	}
	if store, ok := writer.store.(*FileStore); ok {
		if err := ensureRunStartedForTest(store, writer.chatID, writer.runID, event.Timestamp); err != nil {
			panic(err)
		}
	}
	writer.OnEvent(event)
}

func normalizeTestMessageSlice(value any, fallbackTs int64) {
	apply := func(message map[string]any) {
		if message == nil {
			return
		}
		normalizeTestPlatformTimestamp(message, "ts", fallbackTs)
	}
	switch typed := value.(type) {
	case []map[string]any:
		for _, message := range typed {
			apply(message)
		}
	case []any:
		for _, raw := range typed {
			message, _ := raw.(map[string]any)
			apply(message)
		}
	}
}

func ensureRunStartedForTest(store *FileStore, chatID, runID string, startedAt int64) error {
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	if _, exists, err := testRecordedRunStart(store, runID); err != nil {
		return err
	} else if exists {
		return nil
	}
	err := store.OnRunStarted(RunStart{ChatID: chatID, RunID: runID, StartedAtMillis: startedAt})
	if errors.Is(err, ErrChatNotFound) {
		// Some StepWriter unit tests intentionally exercise a writer without a
		// persisted chat. They never replay history, so there is no lifecycle
		// row to construct for that isolated writer fixture.
		return nil
	}
	return err
}

func testRecordedRunStart(store *FileStore, runID string) (int64, bool, error) {
	if store == nil || strings.TrimSpace(runID) == "" {
		return 0, false, nil
	}
	var startedAt int64
	err := store.db.QueryRow(`SELECT STARTED_AT_ FROM RUNS WHERE RUN_ID_=?`, runID).Scan(&startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return startedAt, true, nil
}

func testRequiredEpochMillis(value int64) int64 {
	if value >= timecontract.MinEpochMillis {
		return value
	}
	return testEpochMillis(value)
}

func normalizeTestPlatformTimestamp(payload map[string]any, field string, fallback int64) {
	if payload == nil {
		return
	}
	value, exists := payload[field]
	if !exists {
		payload[field] = fallback
		return
	}
	payload[field] = normalizeTestEpochValue(value)
}

func normalizeTestEpochValue(value any) any {
	toEpoch := func(value int64) int64 {
		if value >= timecontract.MinEpochMillis {
			return value
		}
		return testEpochMillis(value)
	}
	switch typed := value.(type) {
	case int:
		return toEpoch(int64(typed))
	case int64:
		return toEpoch(typed)
	case int32:
		return toEpoch(int64(typed))
	case int16:
		return toEpoch(int64(typed))
	case int8:
		return toEpoch(int64(typed))
	case uint:
		return toEpoch(int64(typed))
	case uint64:
		if typed <= math.MaxInt64 {
			return toEpoch(int64(typed))
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return toEpoch(parsed)
		}
	case float64:
		if math.Trunc(typed) == typed && typed >= math.MinInt64 && typed <= math.MaxInt64 {
			return toEpoch(int64(typed))
		}
	}
	return value
}
