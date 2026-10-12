package queryinput

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSteerRejectsUnknownAndInternalFields(t *testing.T) {
	for _, field := range []string{"unexpectedOption", "source", "preparedMessages", "commitReferences"} {
		for _, value := range []string{"null", "[]", "true"} {
			var request SteerRequest
			if err := json.Unmarshal([]byte(`{"runId":"run","message":"read","`+field+`":`+value+`}`), &request); !errors.Is(err, ErrUnknownField) {
				t.Fatalf("field=%s value=%s error=%v", field, value, err)
			}
		}
	}
	var request SteerRequest
	if err := json.Unmarshal([]byte(`{"requestId":"req","runId":"run","message":"read","references":[{"type":"skill","id":"review"}]}`), &request); err != nil {
		t.Fatal(err)
	}
}
