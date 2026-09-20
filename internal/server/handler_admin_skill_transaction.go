package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type adminSkillTransactionRequest struct {
	Key              string `json:"key"`
	Operation        string `json:"operation"`
	ExpectedRevision string `json:"expectedRevision"`
	Archive          []byte `json:"archiveBase64"`
}

func (s *Server) handleAdminSkillTransaction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, (catalog.EditableSkillMaxUploadBytes*4/3)+(1<<20))
	var request adminSkillTransactionRequest
	decoder := json.NewDecoder(r.Body)
	decodeErr := decoder.Decode(&request)
	if decodeErr == nil {
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			decodeErr = err
			if decodeErr == nil {
				decodeErr = errors.New("multiple JSON values")
			}
		}
	}
	if decodeErr != nil {
		status := http.StatusBadRequest
		var limit *http.MaxBytesError
		if errors.As(decodeErr, &limit) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, api.Failure(status, "invalid skill transaction body"))
		return
	}
	result, err := s.transactAdminSkill(r.Context(), request)
	s.writeAgentHTTPResponse(w, result, err)
}

func (s *Server) transactAdminSkill(ctx context.Context, req adminSkillTransactionRequest) (catalog.EditableSkillSnapshot, error) {
	var prepared *catalog.PreparedEditableSkill
	if req.Operation == "replace" {
		if strings.TrimSpace(req.ExpectedRevision) == "" || len(req.Archive) == 0 {
			return catalog.EditableSkillSnapshot{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "expectedRevision and archiveBase64 are required")
		}
		registry, err := s.adminSkillRegistry()
		if err != nil {
			return catalog.EditableSkillSnapshot{}, err
		}
		prepared, err = registry.PrepareEditableSkillArchive(req.Key, bytes.NewReader(req.Archive), int64(len(req.Archive)))
		if err != nil {
			return catalog.EditableSkillSnapshot{}, mapSkillEditError(err)
		}
		defer prepared.Close()
	}

	transact := func(ctx context.Context) (catalog.EditableSkillSnapshot, error) {
		var empty catalog.EditableSkillSnapshot
		if req.Operation != "snapshot" && req.Operation != "replace" && req.Operation != "delete" {
			return empty, newAgentStatusError(http.StatusBadRequest, "invalid_request", "unknown skill transaction operation")
		}
		registry, err := s.adminSkillRegistry()
		if err != nil {
			return empty, err
		}
		snapshots, ok := registry.(interface {
			SnapshotEditableSkill(string) (catalog.EditableSkillSnapshot, error)
		})
		if !ok {
			return empty, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "skill transactions unavailable")
		}
		current, err := snapshots.SnapshotEditableSkill(req.Key)
		if err != nil {
			return empty, mapSkillEditError(err)
		}
		if req.Operation == "snapshot" {
			return current, nil
		}
		if strings.TrimSpace(req.ExpectedRevision) == "" {
			return empty, newAgentStatusError(http.StatusBadRequest, "invalid_request", "expectedRevision is required")
		}
		if req.ExpectedRevision != current.Revision {
			return empty, newAgentStatusError(http.StatusConflict, "revision_conflict", "skill changed since snapshot")
		}
		var mutation interface {
			Commit() error
			Rollback() error
			SnapshotSkill(string) (catalog.EditableSkillSnapshot, error)
		}
		switch req.Operation {
		case "replace":
			if len(req.Archive) == 0 {
				return empty, newAgentStatusError(http.StatusBadRequest, "invalid_request", "archiveBase64 is required")
			}
			m, _, beginErr := prepared.Begin(true)
			if beginErr != nil {
				return empty, mapSkillEditError(beginErr)
			}
			mutation = m
		case "delete":
			if !current.Exists {
				current.Archive = nil
				return current, nil
			}
			usage, usageErr := registry.EditableSkillUsage(req.Key)
			if usageErr != nil {
				return empty, mapSkillEditError(usageErr)
			}
			if len(usage) > 0 {
				return empty, newAgentStatusErrorWithData(http.StatusConflict, "conflict", "skill is used by agents", map[string]any{"usedByAgents": usage})
			}
			m, beginErr := registry.BeginDeleteEditableSkill(req.Key)
			if beginErr != nil {
				return empty, mapSkillEditError(beginErr)
			}
			mutation = m
		}
		rollback := func(cause error) (catalog.EditableSkillSnapshot, error) {
			if err := mutation.Rollback(); err != nil {
				return empty, fmt.Errorf("skill transaction: %w; rollback failed: %v", cause, err)
			}
			if err := s.reloadAdminSkills(context.WithoutCancel(ctx)); err != nil {
				return empty, fmt.Errorf("skill transaction: %w; restored catalog reload failed: %v", cause, err)
			}
			return empty, cause
		}
		if err := s.reloadAdminSkills(ctx); err != nil {
			return rollback(err)
		}
		result, err := mutation.SnapshotSkill(req.Key)
		if err != nil {
			return rollback(err)
		}
		if err := mutation.Commit(); err != nil {
			return empty, err
		}
		result.Archive = nil
		return result, nil
	}
	if req.Operation == "replace" || req.Operation == "delete" {
		return withCatalogDirectoryTransaction(ctx, s, "skills", transact)
	}
	return withCatalogTransaction(ctx, s, transact)
}
