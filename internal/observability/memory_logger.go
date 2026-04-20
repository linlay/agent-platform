package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var memoryLogger struct {
	mu      sync.Mutex
	enabled bool
	file    *os.File
}

func InitMemoryLogger(enabled bool, path string) error {
	memoryLogger.mu.Lock()
	defer memoryLogger.mu.Unlock()

	if memoryLogger.file != nil {
		_ = memoryLogger.file.Close()
		memoryLogger.file = nil
	}
	memoryLogger.enabled = enabled
	if !enabled || path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	memoryLogger.file = file
	return nil
}

func CloseMemoryLogger() error {
	memoryLogger.mu.Lock()
	defer memoryLogger.mu.Unlock()
	if memoryLogger.file == nil {
		return nil
	}
	err := memoryLogger.file.Close()
	memoryLogger.file = nil
	return err
}

func LogMemoryOperation(operation string, fields map[string]any) {
	memoryLogger.mu.Lock()
	defer memoryLogger.mu.Unlock()
	if !memoryLogger.enabled || memoryLogger.file == nil {
		return
	}
	payload := map[string]any{
		"ts":        time.Now().Format(time.RFC3339Nano),
		"category":  "memory.operation",
		"operation": operation,
	}
	for key, value := range fields {
		switch key {
		case "ts", "category", "operation":
			payload["field."+key] = sanitizeMemoryValue(value)
		default:
			payload[key] = sanitizeMemoryValue(value)
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = memoryLogger.file.Write(append(data, '\n'))
}

func sanitizeMemoryValue(value any) any {
	switch typed := value.(type) {
	case string:
		return SanitizeLog(typed)
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, SanitizeLog(item))
		}
		return out
	default:
		return value
	}
}
