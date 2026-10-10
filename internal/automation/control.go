package automation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// ControlPlan freezes a validated definition and its exact source baseline.
type ControlPlan struct {
	ExecutionOptions map[string]any                `json:"executionOptions,omitempty"`
	Action           string                        `json:"action"`
	Digest           string                        `json:"digest"`
	RequestDigest    string                        `json:"requestDigest"`
	Before           *api.AutomationDetailResponse `json:"before,omitempty"`
	After            *api.AutomationDetailResponse `json:"after,omitempty"`
	Preview          []string                      `json:"preview"`
	Definition       Definition                    `json:"-"`
	Revision         string                        `json:"baseRevision"`
}
type controlReceipt struct {
	RequestDigest string      `json:"requestDigest"`
	Plan          ControlPlan `json:"plan"`
	State         string      `json:"state"`
	Result        any         `json:"result,omitempty"`
}

func digestJSON(v any) string {
	b, _ := json.Marshal(v)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func definitionRevision(d Definition) string { return digestJSON(d) }
func strictDecode(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("automation request exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
func (s *Service) receiptPath(key string) (string, error) {
	if strings.TrimSpace(key) == "" || s.ReceiptDir == "" {
		return "", fmt.Errorf("automation control receipt storage unavailable")
	}
	return filepath.Join(s.ReceiptDir, digestJSON(key)+".json"), nil
}
func readControlReceipt(path string) (*controlReceipt, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r controlReceipt
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
func (s *Service) PrepareControl(action string, args map[string]any, key string) (*ControlPlan, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return nil, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	return s.prepareControl(action, args, key)
}
func (s *Service) prepareControl(action string, args map[string]any, key string) (*ControlPlan, error) {
	requestDigest := digestJSON([]any{action, args})
	if key != "" {
		path, err := s.receiptPath(key)
		if err != nil {
			return nil, err
		}
		receipt, err := readControlReceipt(path)
		if err != nil {
			return nil, err
		}
		if receipt != nil {
			if receipt.RequestDigest != requestDigest {
				return nil, fmt.Errorf("invocation_conflict: this invocation already used different parameters")
			}
			if receipt.State != "completed" {
				return nil, &contracts.MutationError{State: "unknown", Err: fmt.Errorf("execution outcome unknown; inspect the task and execution history before a new invocation")}
			}
			return &receipt.Plan, nil
		}
	}
	p := &ControlPlan{Action: action, RequestDigest: requestDigest, Preview: []string{}}
	var def Definition
	if action == "create" {
		var req api.CreateAutomationRequest
		if err := strictDecode(args, &req); err != nil {
			return nil, err
		}
		// Tool invocation identity supplies a stable creation ID across retries.
		id := "automation-" + digestJSON(key)[:24]
		if key == "" {
			id = "preview"
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		def = Definition{ID: id, Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Enabled: enabled, Cron: strings.TrimSpace(req.Cron), AgentKey: strings.TrimSpace(req.AgentKey), Environment: Environment{ZoneID: strings.TrimSpace(req.ZoneID)}, RemainingRuns: cloneIntPtr(req.RemainingRuns), Query: automationQueryFromRequest(req.Query), SourceFile: filepath.Join(s.Registry.Root(), id+".yml")}
	} else {
		id, _ := args["id"].(string)
		revision, _ := args["baseRevision"].(string)
		var err error
		def, err = s.FindAutomation(id)
		if err != nil {
			return nil, err
		}
		p.Revision = definitionRevision(def)
		if revision == "" || revision != p.Revision {
			return nil, fmt.Errorf("revision_conflict: read automation_query.get and use its baseRevision")
		}
		before, err := s.detailForDefinition(def)
		if err != nil {
			return nil, err
		}
		p.Before = &before
		switch action {
		case "update":
			patch := map[string]any{}
			for k, v := range args {
				if k != "baseRevision" {
					patch[k] = v
				}
			}
			var req api.UpdateAutomationRequest
			if err := strictDecode(patch, &req); err != nil {
				return nil, err
			}
			applyAutomationUpdate(&def, req)
		case "setEnabled":
			enabled, ok := args["enabled"].(bool)
			if !ok {
				return nil, fmt.Errorf("enabled is required")
			}
			def.Enabled = enabled
		case "delete", "trigger":
		default:
			return nil, fmt.Errorf("unsupported automation action")
		}
	}
	if err := s.Registry.Validate(def); err != nil {
		return nil, err
	}
	// Keep an omitted zone omitted in the source, as with YAML and HTTP writes.
	// The effective zone in After is bound to approval separately below.
	def.Query.AccessLevel, _ = normalizeAutomationAccessLevel(def.Query.AccessLevel)
	if def.Query.AccessLevel == "" {
		def.Query.AccessLevel = "default"
	}
	def.Query.Role, _ = normalizeAutomationQueryRole(def.Query.Role)
	p.Definition = def
	if def.Query.RequestID != "" || len(def.Query.References) > 0 || def.Query.Scene != nil {
		p.ExecutionOptions = map[string]any{"requestId": def.Query.RequestID, "references": def.Query.References, "scene": def.Query.Scene}
	}
	after, err := s.detailForDefinition(def)
	if err != nil {
		return nil, err
	}
	p.After = &after
	if action != "delete" {
		schedule, err := parseCronAutomation(def.Cron)
		if err != nil {
			return nil, err
		}
		loc := resolveAutomationLocation(def.Environment.ZoneID, s.DefaultZoneID)
		next := time.Now().In(loc)
		count := 3
		if def.RemainingRuns != nil && *def.RemainingRuns < count {
			count = *def.RemainingRuns
		}
		for i := 0; i < count; i++ {
			next = schedule.Next(next)
			if next.IsZero() {
				break
			}
			p.Preview = append(p.Preview, next.Format(time.RFC3339))
		}
	}
	p.Digest = digestJSON([]any{action, args, p.Revision, def, p.After.ZoneID})
	return p, nil
}
func (s *Service) detailForDefinition(def Definition) (api.AutomationDetailResponse, error) {
	summary, err := s.MapAutomationSummary(def, nil)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	summary.SourceFile = ""
	summary.LastExecution = nil
	summary.ZoneID = resolveAutomationLocation(def.Environment.ZoneID, s.DefaultZoneID).String()
	return api.AutomationDetailResponse{AutomationSummaryResponse: summary, Query: mapAutomationQuery(def.Query)}, nil
}

// ExecuteControl serializes HTTP/tool management and writes a durable intent before any side effect.
// An interrupted intent is never replayed automatically after a process restart.
func (s *Service) ExecuteControl(action string, args map[string]any, key string, authorize func(string) bool) (any, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return nil, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	p, err := s.prepareControl(action, args, key)
	if err != nil {
		return nil, err
	}
	if !authorize(p.Digest) {
		return nil, fmt.Errorf("approval_required: approve this exact invocation and baseline")
	}
	path, err := s.receiptPath(key)
	if err != nil {
		return nil, err
	}
	receipt, err := readControlReceipt(path)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		return receipt.Result, nil
	}
	receipt = &controlReceipt{RequestDigest: p.RequestDigest, Plan: *p, State: "started"}
	if err = os.MkdirAll(s.ReceiptDir, 0700); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(receipt)
	if err = writeFileAtomic(path, b, 0600); err != nil {
		return nil, err
	}
	var result any
	if action == "trigger" {
		// Check the latest baseline, then dispatch the approved immutable snapshot.
		err = s.Registry.checkControlRevision(p.Definition.ID, p.Revision)
		if err == nil {
			var execution Execution
			// Manual execution freezes the reviewed effective zone without saving it.
			snapshot := p.Definition
			snapshot.Environment.ZoneID = p.After.ZoneID
			execution, err = s.Orchestrator.triggerDefinition(snapshot)
			if err == nil {
				result = api.TriggerAutomationResponse{Accepted: true, Status: "accepted", AutomationID: execution.AutomationID, ExecutionID: execution.ID}
			}
		}
	} else {
		err = s.Registry.applyControlDefinition(p.Definition, p.Revision, action == "delete")
		if err == nil {
			err = s.ReloadAutomations()
		}
		if err == nil {
			if action == "delete" {
				result = map[string]any{"id": p.Definition.ID, "deleted": true, "executionHistoryRetained": true}
			} else {
				result, err = s.ControlGet(p.Definition.ID)
			}
		}
	}
	if err != nil {
		return nil, &contracts.MutationError{State: "unknown", Err: err}
	}
	receipt.State = "completed"
	receipt.Result = result
	b, _ = json.Marshal(receipt)
	if err = writeFileAtomic(path, b, 0600); err != nil {
		return nil, &contracts.MutationError{State: "unknown", Err: err}
	}
	return result, nil
}
func (s *Service) ControlGet(id string) (map[string]any, error) {
	def, err := s.FindAutomation(id)
	if err != nil {
		return nil, err
	}
	detail, err := s.detailForDefinition(def)
	if err != nil {
		return nil, err
	}
	return map[string]any{"automation": detail, "baseRevision": definitionRevision(def)}, nil
}
func (r *Registry) checkControlRevision(id, revision string) error {
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	return r.checkControlRevisionLocked(id, revision)
}
func (r *Registry) checkControlRevisionLocked(id, revision string) error {
	defs, err := r.Load()
	if err != nil {
		return err
	}
	for _, d := range defs {
		if d.ID == id {
			if revision == "" || definitionRevision(d) != revision {
				return fmt.Errorf("revision_conflict: automation changed")
			}
			return nil
		}
	}
	if revision != "" {
		return fmt.Errorf("revision_conflict: automation no longer exists")
	}
	// Invalid source files are not returned by Load; never overwrite them on create.
	for _, ext := range []string{".yml", ".yaml"} {
		_, err = os.Lstat(filepath.Join(r.root, id+ext))
		if err == nil {
			return fmt.Errorf("automation source already exists")
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (r *Registry) applyControlDefinition(def Definition, revision string, remove bool) error {
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	if err := r.checkControlRevisionLocked(def.ID, revision); err != nil {
		return err
	}
	if remove {
		return r.deleteDefinition(def)
	}
	return r.persistDefinition(def)
}
