package kbx

import "testing"

func TestDecodeEnvelopeRejectsUnsupportedAndErrorResponses(t *testing.T) {
	for _, input := range []string{
		`[]`,
		`{"schemaVersion":1,"type":"kbx.agent.response","status":"ok","data":{}}`,
		`{"schemaVersion":2,"type":"other","status":"ok","data":{}}`,
		`{"schemaVersion":2,"type":"kbx.agent.response","status":"error","data":{}}`,
		`{"schemaVersion":2,"type":"kbx.agent.response","status":"ok","data":null}`,
	} {
		var result map[string]any
		if err := decodeEnvelope([]byte(input), &result); err == nil {
			t.Errorf("accepted invalid response %s", input)
		}
	}
	var result struct {
		Value int `json:"value"`
	}
	if err := decodeEnvelope([]byte(`{"schemaVersion":2,"type":"kbx.agent.response","status":"ok","data":{"value":3}}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != 3 {
		t.Fatal("data was not decoded")
	}
}
func TestBoundedOutputReportsTruncation(t *testing.T) {
	var b boundedBuffer
	data := make([]byte, 17<<20)
	n, err := b.Write(data)
	if err != nil || n != len(data) || !b.exceeded || b.Len() != 16<<20 {
		t.Fatal("output limit is not enforced")
	}
}
