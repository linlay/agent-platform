package platformcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func archiveImportInvocation(e *contracts.ExecutionContext) string {
	if e == nil || e.CurrentToolID == "" || e.Session.RunID == "" {
		return ""
	}
	data, _ := json.Marshal([]string{e.Session.Subject, e.Session.AgentKey, e.Session.RunID, e.CurrentToolID, "catalog_manage.import"})
	return string(data)
}

func archiveImportRequest(p map[string]any) adminsource.ArchiveImportRequest {
	overwrite, _ := p["overwrite"].(bool)
	return adminsource.ArchiveImportRequest{ResourceType: stringValue(p, "resourceType"), ArchivePath: stringValue(p, "archivePath"), Overwrite: overwrite}
}

func archiveImportRequestDigest(p map[string]any) string {
	data, _ := json.Marshal(archiveImportRequest(p))
	return string(data)
}

// Access admission precedes every archive read, including approval preparation.
// Snapshot bytes are private temporary data, never injected into model arguments.
func (h *ToolHandler) prepareArchiveImport(ctx context.Context, p map[string]any, e *contracts.ExecutionContext) (*adminsource.ArchiveImportPlan, filetools.AccessPlan, error) {
	var access filetools.AccessPlan
	if h.sources == nil || archiveImportInvocation(e) == "" {
		return nil, access, fmt.Errorf("catalog import requires a configured service and invocation identity")
	}
	req := archiveImportRequest(p)
	session := e.Session
	if e.AccessLevel != "" {
		session.AccessLevel = e.AccessLevel
	}
	if h.cfg.Paths.ChatsDir != "" {
		session.RuntimeContext.LocalPaths.ChatsDir = h.cfg.Paths.ChatsDir
	}
	session.ProtectedPaths = append(append([]string(nil), session.ProtectedPaths...), accesspolicy.PlatformProtectedPaths(h.cfg)...)
	access, err := filetools.BuildAccessPlanFromPolicy(h.cfg.AccessPolicy, session, filetools.ReadAccess, req.ArchivePath)
	if err != nil {
		return nil, access, err
	}
	if access.Blocked || filetools.IsBlockedDeviceFile(access.Path) {
		return nil, access, fmt.Errorf("archive_path_blocked: %s", access.Reason)
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.HasReadApproval(e, access) {
		return nil, access, fmt.Errorf("archive_read_approval_required: approve reading this ZIP before package review")
	}
	if !strings.EqualFold(filepath.Ext(access.Path), ".zip") {
		return nil, access, fmt.Errorf("archivePath must name one ZIP file; discover a directory's candidates first")
	}
	info, err := os.Lstat(access.Path)
	if err != nil {
		return nil, access, err
	}
	if !info.Mode().IsRegular() {
		return nil, access, fmt.Errorf("archivePath must be a regular ZIP file")
	}
	file, err := os.Open(access.Path)
	if err != nil {
		return nil, access, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, access, fmt.Errorf("archive changed while opening")
	}
	limit := connector.MaxArchiveUploadBytes
	if req.ResourceType == "skill" {
		limit = catalog.EditableSkillPackageMaxUploadBytes
	}
	if opened.Size() <= 0 || opened.Size() > limit {
		return nil, access, fmt.Errorf("archive exceeds upload size limit or is empty")
	}
	snapshot, err := os.CreateTemp("", "platform-catalog-import-*.zip")
	if err != nil {
		return nil, access, err
	}
	defer os.Remove(snapshot.Name())
	defer snapshot.Close()
	size, err := io.Copy(snapshot, io.LimitReader(file, limit+1))
	if err != nil {
		return nil, access, err
	}
	after, err := os.Lstat(access.Path)
	if err != nil || !os.SameFile(opened, after) || size != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, access, fmt.Errorf("archive changed while reading; retry with a stable ZIP")
	}
	if err := ctx.Err(); err != nil {
		return nil, access, err
	}
	req.ArchivePath = access.Path
	plan, err := h.sources.PrepareArchiveImport(ctx, req, snapshot, size)
	return plan, access, err
}

func (h *ToolHandler) prepareArchiveImportApproval(ctx context.Context, p map[string]any, e *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	if h.sources == nil {
		return nil, fmt.Errorf("catalog import service unavailable")
	}
	if _, err, found := h.sources.ImportReceipt(archiveImportInvocation(e), archiveImportRequestDigest(p)); found {
		return nil, err
	}
	plan, _, err := h.prepareArchiveImport(ctx, p, e)
	if err != nil {
		return nil, err
	}
	defer plan.Close()
	before, _ := json.Marshal(plan.Before)
	after, _ := json.Marshal(plan.After)
	return &contracts.ToolApproval{AllowAutoApprove: true, Fingerprint: contracts.ToolApprovalFingerprint(e, "catalog_manage", "import", plan.Digest), Title: "Import " + plan.Kind, Form: map[string]any{
		"action": "import", "resourceType": plan.Request.ResourceType, "resourceKey": plan.ResourceKey, "kind": plan.Kind,
		"archivePath": plan.Request.ArchivePath, "archiveSha256": plan.ArchiveSHA256, "version": plan.Version,
		"overwrite": plan.Request.Overwrite, "baseRevision": plan.BaseRevision, "before": string(before), "after": string(after),
	}}, nil
}

func (h *ToolHandler) invokeArchiveImport(ctx context.Context, p map[string]any, e *contracts.ExecutionContext) (any, error) {
	if h.sources == nil {
		return nil, fmt.Errorf("catalog import service unavailable")
	}
	key, requestDigest := archiveImportInvocation(e), archiveImportRequestDigest(p)
	if result, err, found := h.sources.ImportReceipt(key, requestDigest); found {
		return result, err
	}
	plan, access, err := h.prepareArchiveImport(ctx, p, e)
	if err != nil {
		return nil, err
	}
	defer plan.Close()
	result, err := h.sources.ApplyArchiveImport(ctx, plan, key, requestDigest, func(digest string) bool {
		if !contracts.ConsumeToolApproval(e, contracts.ToolApprovalFingerprint(e, "catalog_manage", "import", digest)) {
			return false
		}
		return access.AllowedByWhitelist || access.AutoApproved || filetools.ConsumeReadApproval(e, access)
	})
	if err == nil && plan.Kind == "connector" {
		h.addConnectorConnection(ctx, plan.ResourceKey, result)
	}
	return result, err
}
