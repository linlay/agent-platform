package connectorops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{8,128}$`)

type receipt struct {
	Digest string  `json:"digest"`
	Result *Result `json:"result,omitempty"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// The claim is durable before dispatch. An incomplete claim is never replayed,
// including after a process crash or lost response. No remote exactly-once
// guarantee is assumed. Completed receipts can be returned to the same caller.
func beginWrite(root string, scope Scope, req Request) (string, *Result, error) {
	if root == "" || !idempotencyKeyPattern.MatchString(req.IdempotencyKey) {
		return "", nil, failure("idempotency_key_required", 400)
	}
	key := digest([]byte(scope.Subject + "\x00" + scope.AppID + "\x00" + req.ConnectorID + "\x00" + "execution-v2" + "\x00" + req.IdempotencyKey))
	args, _ := json.Marshal(struct {
		Adapter   string
		Args      []string
		Component string
		ToolName  string
		Arguments map[string]any
	}{req.Adapter, req.Args, req.Component, req.ToolName, req.Arguments})
	fingerprint := digest(args)
	dir := filepath.Join(root, req.ConnectorID, "invocations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, failure("receipt_unavailable", 503)
	}
	file := filepath.Join(dir, key+".json")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		raw, e := os.ReadFile(file)
		var claim receipt
		if e != nil || json.Unmarshal(raw, &claim) != nil {
			return "", nil, failure("invocation_outcome_unknown", 409)
		}
		if claim.Digest != fingerprint {
			return "", nil, failure("idempotency_conflict", 409)
		}
		raw, e = os.ReadFile(file + ".done")
		if e != nil || len(raw) > MaxJSONBytes || json.Unmarshal(raw, &claim) != nil || claim.Result == nil {
			return "", nil, failure("invocation_outcome_unknown", 409)
		}
		if claim.Digest != fingerprint {
			return "", nil, failure("idempotency_conflict", 409)
		}
		return "", claim.Result, nil
	}
	if err != nil {
		return "", nil, failure("receipt_unavailable", 503)
	}
	defer f.Close()
	data, _ := json.Marshal(receipt{Digest: fingerprint})
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return "", nil, failure("receipt_unavailable", 503)
	}
	return file, nil, nil
}
func finishWrite(file string, req Request, result Result) error {
	args, _ := json.Marshal(struct {
		Adapter   string
		Args      []string
		Component string
		ToolName  string
		Arguments map[string]any
	}{req.Adapter, req.Args, req.Component, req.ToolName, req.Arguments})
	data, err := json.Marshal(receipt{Digest: digest(args), Result: &result})
	if err != nil || len(data) > MaxJSONBytes {
		return failure("invocation_outcome_unknown", 409)
	}
	// A torn receipt is treated as unknown; the original durable claim remains.
	f, err := os.OpenFile(file+".done", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return failure("invocation_outcome_unknown", 409)
	}
	defer f.Close()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return failure("invocation_outcome_unknown", 409)
	}
	return nil
}
