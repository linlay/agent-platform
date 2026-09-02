package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

const (
	documentCommitMaxBinaryBytes = 100 * 1024 * 1024
	documentCommitMaxBodyBytes   = 140 * 1024 * 1024
)

func cleanDocumentCommitText(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return ""
	}
	for _, char := range value {
		if char == 0 || char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return value
}

func decodeDocumentCommitPayload(request api.DocumentCommitRequest, maxTextBytes int) ([]byte, error) {
	kind := request.Payload.Kind
	if !documentKindEditable(kind) {
		return nil, errors.New("document type is read-only")
	}
	if kind == documentKindImage {
		if request.Payload.Text != nil || strings.TrimSpace(request.Payload.DataBase64) == "" {
			return nil, errors.New("image document requires dataBase64")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(request.Payload.DataBase64))
		if err != nil || len(data) == 0 {
			return nil, errors.New("invalid image document payload")
		}
		if len(data) > documentCommitMaxBinaryBytes {
			return nil, errors.New("document payload too large")
		}
		return data, nil
	}
	if request.Payload.Text == nil || strings.TrimSpace(request.Payload.DataBase64) != "" {
		return nil, errors.New("text document requires text payload")
	}
	if encoding := strings.ToLower(strings.TrimSpace(request.Payload.Encoding)); encoding != "" && encoding != "utf-8" && encoding != "utf8" {
		return nil, errors.New("only UTF-8 document commits are supported")
	}
	data := []byte(*request.Payload.Text)
	if len(data) > maxTextBytes {
		return nil, errors.New("document payload too large")
	}
	return data, nil
}

func (s *Server) handleDocumentCommit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, documentCommitMaxBodyBytes)
	defer r.Body.Close()
	var request api.DocumentCommitRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, api.Failure(status, "invalid document.commit payload"))
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid document.commit payload"))
		return
	}
	request.Operation = cleanDocumentCommitText(request.Operation, 64)
	request.Source.Kind = cleanDocumentCommitText(request.Source.Kind, 32)
	request.Source.AgentKey = cleanDocumentCommitText(request.Source.AgentKey, 512)
	request.Source.Path = cleanDocumentCommitText(request.Source.Path, 2048)
	request.Source.ChatID = cleanDocumentCommitText(request.Source.ChatID, 512)
	request.Source.ResourceID = cleanDocumentCommitText(request.Source.ResourceID, 1024)
	request.Source.RelativePath = cleanDocumentCommitText(request.Source.RelativePath, 2048)
	request.Mode = cleanDocumentCommitText(request.Mode, 32)
	request.ExpectedRevision = cleanDocumentCommitText(request.ExpectedRevision, 512)
	request.Payload.Kind = cleanDocumentCommitText(request.Payload.Kind, 64)
	request.Payload.MIMEType = normalizeDocumentMIME(request.Payload.MIMEType)
	maxTextBytes := s.deps.Config.FileTools.MaxReadBytes
	if maxTextBytes <= 0 {
		maxTextBytes = 1 << 20
	}
	data, err := decodeDocumentCommitPayload(request, maxTextBytes)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, api.Failure(status, err.Error()))
		return
	}
	if request.Operation != "document.commit" || request.Source.AgentKey == "" || request.ExpectedRevision == "" || request.Payload.MIMEType == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid document.commit request"))
		return
	}
	switch request.Source.Kind {
	case "workspace-file":
		s.commitWorkspaceDocument(w, request, data)
	case "artifact", "reference":
		s.commitResourceDocument(w, r, request, data)
	default:
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid document source"))
	}
}

func stageWorkspaceDocument(targetPath string, mode os.FileMode, data []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".document-commit-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close() }()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (s *Server) commitWorkspaceDocument(w http.ResponseWriter, request api.DocumentCommitRequest, data []byte) {
	if request.Mode != "overwrite" || request.Source.Path == "" || request.Source.ChatID != "" || request.Source.ResourceID != "" || request.Source.RelativePath != "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid workspace document commit"))
		return
	}
	resolved, err := s.resolveAgentFile(request.Source.AgentKey, request.Source.Path)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	sourceSample, _, err := readAgentFilePrefix(resolved.AbsolutePath, 512)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "document source could not be read"))
		return
	}
	currentKind := classifyDocumentKind(resolved.Info.Name(), detectDocumentMIME(resolved.AbsolutePath), sourceSample)
	if !documentKindEditable(currentKind) || currentKind != request.Payload.Kind ||
		!validDocumentCommitResult(resolved.Info.Name(), request.Payload.Kind, request.Payload.MIMEType, data) {
		writeJSON(w, http.StatusUnsupportedMediaType, api.Failure(http.StatusUnsupportedMediaType, "document type cannot be overwritten"))
		return
	}
	if agentFileRevision(resolved.Info) != request.ExpectedRevision {
		writeJSON(w, http.StatusConflict, api.Failure(http.StatusConflict, "document revision conflict", map[string]any{"code": "revision_conflict"}))
		return
	}
	staged, err := stageWorkspaceDocument(resolved.AbsolutePath, resolved.Info.Mode(), data)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "document commit failed"))
		return
	}
	defer os.Remove(staged)
	if err := chat.AtomicReplaceFile(staged, resolved.AbsolutePath); err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "document commit failed"))
		return
	}
	info, err := os.Stat(resolved.AbsolutePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "document commit failed"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.DocumentCommitResponse{
		SourceKind: "workspace-file", AgentKey: resolved.AgentKey, Path: resolved.Path,
		Revision: agentFileRevision(info), DocumentKind: request.Payload.Kind, MIMEType: request.Payload.MIMEType,
	}))
}

func (s *Server) commitResourceDocument(w http.ResponseWriter, r *http.Request, request api.DocumentCommitRequest, data []byte) {
	if request.Source.ChatID == "" || request.Source.ResourceID == "" || request.Source.RelativePath == "" || request.Source.Path != "" ||
		(request.Mode != "overwrite" && request.Mode != "new-artifact") {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid resource document commit"))
		return
	}
	principal := PrincipalFromContext(r.Context())
	if principal != nil && !s.principalCanAccessResourceChat(principal, request.Source.ChatID) {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	summary, err := s.deps.Chats.Summary(request.Source.ChatID)
	if err != nil || summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat resource not found"))
		return
	}
	if strings.TrimSpace(summary.TeamID) != "" || strings.TrimSpace(summary.AgentKey) != request.Source.AgentKey {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource owner mismatch"))
		return
	}
	committer, ok := s.deps.Chats.(chat.ResourceDocumentCommitter)
	if !ok || committer == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "resource document commit is unavailable"))
		return
	}
	result, err := committer.CommitResourceDocument(chat.ResourceDocumentCommitRequest{
		ChatID: request.Source.ChatID, Profile: request.Source.Kind, ResourceID: request.Source.ResourceID,
		RelativePath: request.Source.RelativePath, Mode: request.Mode, ExpectedRevision: request.ExpectedRevision,
		DocumentKind: request.Payload.Kind, MIMEType: request.Payload.MIMEType, Data: data,
	})
	if err != nil {
		status, message, code := http.StatusInternalServerError, "resource document commit failed", "commit_failed"
		switch {
		case errors.Is(err, chat.ErrResourceDocumentInvalid):
			status, message, code = http.StatusBadRequest, "invalid resource document commit", "invalid_request"
		case errors.Is(err, chat.ErrResourceDocumentOverwriteDenied):
			status, message, code = http.StatusForbidden, "Reference resources cannot be overwritten", "overwrite_denied"
		case errors.Is(err, chat.ErrResourceDocumentIdentityMismatch):
			status, message, code = http.StatusForbidden, "resource identity mismatch", "identity_mismatch"
		case errors.Is(err, chat.ErrResourceDocumentRevisionConflict):
			status, message, code = http.StatusConflict, "document revision conflict", "revision_conflict"
		case errors.Is(err, chat.ErrChatNotFound), errors.Is(err, os.ErrNotExist):
			status, message, code = http.StatusNotFound, "resource not found", "not_found"
		case errors.Is(err, os.ErrPermission):
			status, message, code = http.StatusForbidden, "resource access denied", "forbidden"
		}
		writeJSON(w, status, api.Failure(status, message, map[string]any{"code": code}))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.DocumentCommitResponse{
		SourceKind: "artifact", AgentKey: request.Source.AgentKey, ChatID: request.Source.ChatID,
		ArtifactID: result.ArtifactID, ResourceID: result.ResourceID, RelativePath: result.RelativePath,
		Revision: result.Revision, DocumentKind: request.Payload.Kind, MIMEType: request.Payload.MIMEType,
	}))
}
