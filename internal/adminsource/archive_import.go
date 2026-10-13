package adminsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mcp"
	"agent-platform/internal/pathutil"
)

// ArchiveImportRequest has no destination: Platform selects the source center.
// ArchivePath identifies the caller-authorized, canonical input, never a target.
type ArchiveImportRequest struct {
	ResourceType string `json:"resourceType"`
	ArchivePath  string `json:"archivePath"`
	Overwrite    bool   `json:"overwrite"`
}

type ArchiveImportPlan struct {
	Request           ArchiveImportRequest
	ResourceKey       string
	Kind              string
	Version           string
	ArchiveSHA256     string
	BaseRevision      string
	Digest            string
	Before, After     map[string]any
	Connector         *connector.Package
	root              string
	candidate         string
	candidateRevision string
	close             func()
	publish           func(context.Context, func() error) error
}

func (p *ArchiveImportPlan) Close() {
	if p != nil && p.close != nil {
		p.close()
	}
}

type archiveImportReceipt struct {
	RequestDigest string
	Result        map[string]any
	Err           error
}

// ImportReceipt returns only the outcome of this exact invocation. It does not
// authorize a new call, read a new archive or repeat preparation commands.
func (s *ControlService) ImportReceipt(invocation, requestDigest string) (map[string]any, error, bool) {
	if invocation == "" {
		return nil, nil, false
	}
	r, ok := s.importReceipts.Load(invocation)
	if !ok {
		return nil, nil, false
	}
	if r.RequestDigest != requestDigest {
		return nil, fmt.Errorf("idempotency_conflict: this invocation already used different import arguments"), true
	}
	return contracts.CloneMap(r.Result), r.Err, true
}

// PrepareArchiveImport uses existing package parsers and unpublished candidates.
// The reader must be an immutable snapshot obtained after file access admission.
func (s *ControlService) PrepareArchiveImport(ctx context.Context, req ArchiveImportRequest, source io.ReaderAt, size int64) (plan *ArchiveImportPlan, err error) {
	p := &ArchiveImportPlan{Request: req, Before: map[string]any{}, After: map[string]any{}}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	if source == nil || size <= 0 {
		return nil, fmt.Errorf("invalid_archive: provide a nonempty ZIP")
	}
	maxBytes := connector.MaxArchiveUploadBytes
	if req.ResourceType == "skill" {
		maxBytes = catalog.EditableSkillPackageMaxUploadBytes
	} else if req.ResourceType != "connector" {
		return nil, fmt.Errorf("resourceType must be skill or connector")
	}
	if size > maxBytes {
		return nil, fmt.Errorf("archive exceeds upload size limit")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(source, 0, size)); err != nil {
		return nil, err
	}
	p.ArchiveSHA256 = hex.EncodeToString(hash.Sum(nil))
	switch req.ResourceType {
	case "connector":
		prepared, err := connector.PrepareArchive(ctx, s.Config.Paths.ConnectorSources(), source, size)
		if err != nil {
			return nil, err
		}
		p.close = prepared.Close
		if err := prepared.Validate(mcp.ValidateConnectorPackages); err != nil {
			return nil, err
		}
		pkg := prepared.Package()
		p.Connector = &pkg
		p.candidate = pkg.Dir
		p.ResourceKey, p.Version, p.Kind = pkg.ID, pkg.Version, "connector"
		p.root = filepath.Join(s.Config.Paths.EffectiveConnectorsCenterDir(), pkg.ID)
		p.After = map[string]any{"id": pkg.ID, "name": pkg.Name, "version": pkg.Version, "type": pkg.Type, "authMode": pkg.AuthMode}
		components := make([]string, 0, len(pkg.MCP))
		for name := range pkg.MCP {
			components = append(components, name)
		}
		sort.Strings(components)
		p.After["mcpComponents"] = components
		skills := make([]string, 0, len(pkg.Skills))
		for _, skill := range pkg.Skills {
			skills = append(skills, skill.Name)
		}
		p.After["skills"] = skills
		if pkg.CLI != nil {
			// Import starts preparation, never login. Show the package-defined
			// runtime/init/version checks because these may execute local code.
			p.After["cliPreparation"] = map[string]any{"runtime": pkg.CLI["runtime"], "init": pkg.CLI["init"], "versionCheck": pkg.CLI["versionCheck"]}
		}
		p.publish = func(ctx context.Context, check func() error) error {
			_, err := prepared.PublishConditional(ctx, req.Overwrite, check, mcp.ValidateConnectorPackages, func() error { return s.reload(context.WithoutCancel(ctx), "connector") })
			var mutation *connector.ArchiveMutationError
			if errors.As(err, &mutation) {
				return &contracts.MutationError{State: mutation.State, Err: err}
			}
			return err
		}
	case "skill":
		registry, ok := s.Registry.(interface {
			PrepareEditableSkillArchive(string, io.ReaderAt, int64) (*catalog.PreparedEditableSkill, error)
			PrepareEditableSkillPackageArchive(string, string, io.ReaderAt, int64) (*catalog.PreparedEditableSkillPackage, error)
		})
		if !ok {
			return nil, fmt.Errorf("skill archive import unavailable")
		}
		id, version, isPackage, err := catalog.DetectSkillPackageArchive(source, size)
		if err != nil {
			return nil, err
		}
		p.Kind = "skill"
		if isPackage {
			prepared, err := registry.PrepareEditableSkillPackageArchive(id, version, source, size)
			if err != nil {
				return nil, err
			}
			p.close = prepared.Close
			record, err := prepared.Preview()
			if err != nil {
				return nil, err
			}
			p.Kind, p.ResourceKey, p.Version = "skill-package", record.ID, record.Version
			p.candidate = prepared.CandidateDirectory()
			members := make([]string, 0, len(record.Skills))
			for _, skill := range record.Skills {
				members = append(members, skill.ID)
			}
			p.After = map[string]any{"id": record.ID, "name": record.DisplayName, "version": record.Version, "skills": members}
			p.publish = func(ctx context.Context, check func() error) error {
				if err := check(); err != nil {
					return err
				}
				mutation, _, err := prepared.Begin()
				return s.finishArchiveSkillMutation(ctx, mutation, err)
			}
		} else {
			id, err = catalog.DetectEditableSkillArchiveID(source, size)
			if err != nil {
				return nil, err
			}
			prepared, err := registry.PrepareEditableSkillArchive(id, source, size)
			if err != nil {
				return nil, err
			}
			p.close = prepared.Close
			skill, err := prepared.Preview()
			if err != nil {
				return nil, err
			}
			p.ResourceKey, p.Version = skill.ID, skill.Version
			p.candidate = skill.Source.SkillDir
			p.After = map[string]any{"id": skill.ID, "name": skill.Name, "version": skill.Version, "instructions": skill.SkillMd}
			p.publish = func(ctx context.Context, check func() error) error {
				if err := check(); err != nil {
					return err
				}
				mutation, _, err := prepared.Begin(req.Overwrite)
				if err != nil {
					return s.finishArchiveSkillMutation(ctx, nil, err)
				}
				return s.finishArchiveSkillMutation(ctx, mutation, nil)
			}
		}
		p.root = filepath.Join(s.Config.Paths.SkillsCenterDir, p.ResourceKey)
	}
	if connector.IsBuiltin(p.ResourceKey) {
		return nil, connector.ErrBuiltinReadOnly
	}
	parent, err := pathutil.Canonicalize(filepath.Dir(p.root))
	if err != nil {
		return nil, err
	}
	p.root = filepath.Join(parent.Host, filepath.Base(p.root))
	if err := noLinks(p.root); err != nil {
		return nil, err
	}
	revision, exists, err := treeRevisionWithLimit(p.root, catalog.EditableSkillPackageMaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	if exists && !req.Overwrite {
		return nil, fmt.Errorf("resource_exists: %s already exists; set overwrite:true to review replacement", p.ResourceKey)
	}
	p.BaseRevision = revision
	p.candidateRevision, _, err = treeRevisionWithLimit(p.candidate, catalog.EditableSkillPackageMaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	p.Before = map[string]any{"id": p.ResourceKey, "exists": exists}
	if exists {
		p.Before["baseRevision"] = revision
	}
	data, err := json.Marshal([]any{req, p.ResourceKey, p.Kind, p.Version, p.ArchiveSHA256, p.candidateRevision, revision, p.After})
	if err != nil {
		return nil, err
	}
	p.Digest = controlHash(data)
	return p, nil
}

func (s *ControlService) finishArchiveSkillMutation(ctx context.Context, mutation interface {
	Commit() error
	Rollback() error
}, beginErr error) error {
	if beginErr != nil {
		state := "not_started"
		var mutation *catalog.SkillArchiveMutationError
		if errors.As(beginErr, &mutation) {
			state = mutation.State
		}
		return &contracts.MutationError{State: state, Err: beginErr}
	}
	if err := s.reload(context.WithoutCancel(ctx), "skill"); err != nil {
		rollbackErr := mutation.Rollback()
		reloadErr := s.reload(context.WithoutCancel(ctx), "skill")
		state := "rolled_back"
		if rollbackErr != nil || reloadErr != nil {
			state = "unknown"
		}
		return &contracts.MutationError{State: state, Err: errors.Join(err, rollbackErr, reloadErr)}
	}
	if err := mutation.Commit(); err != nil {
		return &contracts.MutationError{State: "committed", Err: err}
	}
	return nil
}

func (s *ControlService) ApplyArchiveImport(ctx context.Context, p *ArchiveImportPlan, invocation, requestDigest string, approve func(string) bool) (result map[string]any, err error) {
	unlock := s.Mutations.LockAgentMutation()
	defer unlock()
	sourceUnlock := s.Mutations.LockSourceMutation()
	defer sourceUnlock()
	if result, err, found := s.ImportReceipt(invocation, requestDigest); found {
		return result, err
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := noLinks(p.root); err != nil {
			return err
		}
		revision, _, err := treeRevisionWithLimit(p.root, catalog.EditableSkillPackageMaxArchiveBytes)
		if err != nil {
			return err
		}
		if revision != p.BaseRevision {
			return fmt.Errorf("revision_conflict: installed resource changed after review")
		}
		candidateRevision, exists, err := treeRevisionWithLimit(p.candidate, catalog.EditableSkillPackageMaxArchiveBytes)
		if err != nil {
			return err
		}
		if !exists || candidateRevision != p.candidateRevision {
			return fmt.Errorf("approval_stale: unpublished package changed after validation")
		}
		return nil
	}
	mutate := func(ctx context.Context) error {
		if err := check(); err != nil {
			return err
		}
		if approve == nil || !approve(p.Digest) {
			return fmt.Errorf("approval_required: approve this exact archive and installed baseline")
		}
		err := p.publish(ctx, check)
		if err == nil {
			result = map[string]any{"status": "applied", "executionState": "committed", "resourceType": p.Request.ResourceType, "resourceKey": p.ResourceKey, "kind": p.Kind, "id": p.ResourceKey, "version": p.Version, "installed": true, "archiveSha256": p.ArchiveSHA256}
			if p.Connector != nil && p.Connector.CLI != nil {
				if s.PrepareConnector == nil {
					result["preparation"] = map[string]any{"connectorId": p.ResourceKey, "status": "failed", "message": "CLI preparation service unavailable; the package was imported."}
				} else {
					preparation, preparationErr := s.PrepareConnector(p.ResourceKey)
					if preparationErr != nil {
						result["preparation"] = map[string]any{"connectorId": p.ResourceKey, "status": "failed", "message": preparationErr.Error()}
					} else {
						result["preparation"] = preparation
					}
				}
			}
			result["baseRevision"], _, err = treeRevisionWithLimit(p.root, catalog.EditableSkillPackageMaxArchiveBytes)
			if err != nil {
				err = &contracts.MutationError{State: "committed", Err: err}
			}
		}
		if invocation != "" {
			s.importReceipts.Store(invocation, archiveImportReceipt{RequestDigest: requestDigest, Result: contracts.CloneMap(result), Err: err})
		}
		return err
	}
	if s.Coordinate != nil {
		reason := "skills"
		if p.Kind == "connector" {
			reason = "connectors"
		}
		err = s.Coordinate(ctx, reason, mutate)
	} else {
		err = mutate(ctx)
	}
	return result, err
}
