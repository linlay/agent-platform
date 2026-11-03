package contracts

import (
	"agent-platform/internal/contracts/queryinput"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// RunStartPlan is the read-only outcome of PrepareRunStart. Preparing a plan
// creates no Chat, registers no Run and holds no lease.
type RunStartPlan struct {
	TargetRevision       string
	AccessSource         string
	RequestedAccessLevel string // empty when the caller omitted accessLevel
	ParentAccessLevel    string
	AccessLevel          string // level the new Run would start with
	RequestDigest        string
}

// NormalizeRunStartRequest resolves an omitted target from the trusted caller
// before idempotency or admission. It never selects a global
// default Agent or infers a target from the requested Chat.
func NormalizeRunStartRequest(request RunStartRequest) (RunStartRequest, error) {
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		return request, &RunToolError{Code: "invalid_request", Message: "message is required"}
	}
	request.AgentKey = strings.TrimSpace(request.AgentKey)
	if request.AgentKey == "" {
		request.AgentKey = strings.TrimSpace(request.Origin.AgentKey)
	}
	if request.AgentKey == "" {
		return request, &RunToolError{Code: "run_context_required", Message: "chat_start requires a caller Agent when agentKey is omitted"}
	}
	return request, nil
}

// AccessLevelRank orders ask_for_approval < auto_approve < full_access.
func AccessLevelRank(level string) int {
	switch normalized, _ := NormalizeAccessLevel(level); normalized {
	case AccessLevelFullAccess:
		return 2
	case AccessLevelAutoApprove:
		return 1
	default:
		return 0
	}
}

// RunStartRequestDigest identifies the start parameters of one invocation.
// It excludes parent permission state so a legitimate retry stays idempotent,
// and keeps omitted accessLevel distinct from an explicit default.
func RunStartRequestDigest(request RunStartRequest) string {
	refs, _ := queryinput.NormalizeResourceReferences(request.References)
	encoded, _ := json.Marshal(refs)
	return digestStrings("run-start-request",
		strings.TrimSpace(request.AgentKey), strings.TrimSpace(request.ChatID),
		strings.TrimSpace(request.Message), strings.TrimSpace(request.AccessLevel), strings.TrimSpace(request.ChatName),
		string(encoded), strings.TrimSpace(request.ModelKey), strings.TrimSpace(request.ReasoningEffort))
}

func digestStrings(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// NormalizeRequiredLibraries preserves first-selection order for prompt and replay.
func NormalizeRequiredLibraries(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}
