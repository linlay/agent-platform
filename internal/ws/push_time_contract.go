package ws

import (
	"reflect"
	"strings"
)

// allowsPushTimestamp enforces the WebSocket push contract: only heartbeat is
// a transport-level clock notification. Every other push must use a
// business-specific time field (for example startedAt or readAt).
func allowsPushTimestamp(eventType string, data any) bool {
	return eventType == "heartbeat" || !containsTimestampField(data)
}

func containsTimestampField(value any) bool {
	return containsTimestampValue(reflect.ValueOf(value))
}

func containsTimestampValue(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		return !value.IsNil() && containsTimestampValue(value.Elem())
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Kind() == reflect.String && key.String() == "timestamp" {
				return true
			}
			if containsTimestampValue(iter.Value()) {
				return true
			}
		}
	case reflect.Array, reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			if containsTimestampValue(value.Index(index)) {
				return true
			}
		}
	case reflect.Struct:
		typeOfValue := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeOfValue.Field(index)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "timestamp" || containsTimestampValue(value.Field(index)) {
				return true
			}
		}
	}
	return false
}
