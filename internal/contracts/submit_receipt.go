package contracts

import (
	"agent-platform/internal/contracts/queryinput"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// SubmitDigest freezes semantic JSON, independent of object key order.
func SubmitDigest(req queryinput.SubmitRequest) string {
	raw, err := json.Marshal(map[string]any{"param": req.Param, "params": req.Params})
	if err != nil {
		return ""
	}
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	raw, _ = json.Marshal(input)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func cloneSubmitParam(param queryinput.SubmitParam) queryinput.SubmitParam {
	if param == nil {
		return nil
	}
	raw, _ := json.Marshal(param)
	var copy queryinput.SubmitParam
	json.Unmarshal(raw, &copy)
	return copy
}
