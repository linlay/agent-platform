package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// RunStartReview is the frozen review a trusted handler prepared for one
// chat_start invocation. It is never model input and is never serialized.
// Once present, Runtime must validate it and consume its one-shot approval,
// even when the parent level no longer makes the request an escalation.
type RunStartReview struct {
	ParentAccessLevel   string
	ParentAccessVersion int64
	ApprovalDigest      string
	// Consume synchronously spends the approval bound to the digest Runtime
	// computed itself. It must come from the trusted execution context.
	Consume func(approvalDigest string) bool
}

// RunStartPlan is the read-only outcome of PrepareRunStart. Preparing a plan
// creates no Chat, registers no Run and holds no lease.
type RunStartPlan struct {
	RequestedAccessLevel string // empty when the caller omitted accessLevel
	ParentAccessLevel    string
	ParentAccessVersion  int64
	AccessLevel          string // level the new Run would start with
	RequiresApproval     bool
	RequestDigest        string
	ApprovalDigest       string
	TargetName           string
	ChatName             string // existing Chat name when continuing
}

// AccessLevelRank orders default < auto_approve < full_access.
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

// ResolveRunStartAccessLevel applies inheritance for an omitted level and
// reports whether the result exceeds the parent Run's current level.
func ResolveRunStartAccessLevel(requested string, parent string) (string, bool) {
	parent, _ = NormalizeAccessLevel(parent)
	if strings.TrimSpace(requested) == "" {
		return parent, false
	}
	level, _ := NormalizeAccessLevel(requested)
	return level, AccessLevelRank(level) > AccessLevelRank(parent)
}

// RunStartRequestDigest identifies the start parameters of one invocation.
// It excludes parent permission state so a legitimate retry stays idempotent,
// and keeps omitted accessLevel distinct from an explicit default.
func RunStartRequestDigest(request RunStartRequest) string {
	skills := make([]string, 0, len(request.MustUseSkills))
	for _, id := range request.MustUseSkills {
		skills = append(skills, strings.TrimSpace(id))
	}
	return digestStrings("run-start-request",
		strings.TrimSpace(request.AgentKey), strings.TrimSpace(request.TeamID), strings.TrimSpace(request.ChatID),
		strings.TrimSpace(request.Message), strings.TrimSpace(request.AccessLevel), strings.TrimSpace(request.ChatName),
		strings.Join(skills, "\x00"))
}

// RunStartApprovalDigest identifies exactly what a reviewer approved: the
// request plus the parent permission baseline it was reviewed against.
func RunStartApprovalDigest(requestDigest string, parentAccessLevel string, parentAccessVersion int64, accessLevel string) string {
	version, _ := json.Marshal(parentAccessVersion)
	return digestStrings("run-start-approval", requestDigest, parentAccessLevel, string(version), accessLevel)
}

func digestStrings(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
