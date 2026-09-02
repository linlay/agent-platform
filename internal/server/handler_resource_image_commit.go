package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

const (
	resourceImageCommitMaxBytes     = 100 * 1024 * 1024
	resourceImageCommitMaxBodyBytes = 140 * 1024 * 1024
)

type resourceImageCommitRequest struct {
	Operation        string `json:"operation"`
	Profile          string `json:"profile"`
	AgentKey         string `json:"agentKey"`
	ChatID           string `json:"chatId"`
	ResourceID       string `json:"resourceId"`
	RelativePath     string `json:"relativePath"`
	Mode             string `json:"mode"`
	ExpectedRevision string `json:"expectedRevision"`
	MIMEType         string `json:"mimeType"`
	DataBase64       string `json:"dataBase64"`
}

func validResourceImageCommitText(value string, max int) string {
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

func decodeResourceImageCommitPayload(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > base64.StdEncoding.EncodedLen(resourceImageCommitMaxBytes) {
		return nil, chat.ErrResourceImageInvalid
	}
	data, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(data) == 0 {
		return nil, chat.ErrResourceImageInvalid
	}
	if len(data) > resourceImageCommitMaxBytes {
		return nil, errors.New("resource image payload too large")
	}
	return data, nil
}

func (s *Server) handleResourceImageCommit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, resourceImageCommitMaxBodyBytes)
	defer r.Body.Close()
	var request resourceImageCommitRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, api.Failure(status, "invalid resource.image.commit payload"))
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid resource.image.commit payload"))
		return
	}

	request.Operation = validResourceImageCommitText(request.Operation, 64)
	request.Profile = validResourceImageCommitText(request.Profile, 32)
	request.AgentKey = validResourceImageCommitText(request.AgentKey, 512)
	request.ChatID = validResourceImageCommitText(request.ChatID, 512)
	request.ResourceID = validResourceImageCommitText(request.ResourceID, 1_024)
	request.RelativePath = validResourceImageCommitText(request.RelativePath, 2_048)
	request.Mode = validResourceImageCommitText(request.Mode, 32)
	request.ExpectedRevision = validResourceImageCommitText(request.ExpectedRevision, 128)
	request.MIMEType = validResourceImageCommitText(request.MIMEType, 64)
	if request.Operation != "resource.image.commit" || request.AgentKey == "" || request.ChatID == "" || request.ResourceID == "" || request.RelativePath == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid resource.image.commit request"))
		return
	}
	principal := PrincipalFromContext(r.Context())
	if principal != nil && !s.principalCanAccessResourceChat(principal, request.ChatID) {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	summary, err := s.deps.Chats.Summary(request.ChatID)
	if err != nil || summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat resource not found"))
		return
	}
	if strings.TrimSpace(summary.TeamID) != "" || strings.TrimSpace(summary.AgentKey) == "" || strings.TrimSpace(summary.AgentKey) != request.AgentKey {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource owner mismatch"))
		return
	}
	data, err := decodeResourceImageCommitPayload(request.DataBase64)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, api.Failure(status, err.Error()))
		return
	}
	committer, ok := s.deps.Chats.(chat.ResourceDocumentCommitter)
	if !ok || committer == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "resource image commit is unavailable"))
		return
	}
	result, err := committer.CommitResourceDocument(chat.ResourceDocumentCommitRequest{
		ChatID:           request.ChatID,
		Profile:          request.Profile,
		ResourceID:       request.ResourceID,
		RelativePath:     request.RelativePath,
		Mode:             request.Mode,
		ExpectedRevision: request.ExpectedRevision,
		DocumentKind:     "document-image",
		MIMEType:         request.MIMEType,
		Data:             data,
	})
	if err != nil {
		status := http.StatusInternalServerError
		message := "resource image commit failed"
		switch {
		case errors.Is(err, chat.ErrResourceDocumentInvalid):
			status, message = http.StatusBadRequest, "invalid resource image commit"
		case errors.Is(err, chat.ErrResourceDocumentOverwriteDenied):
			status, message = http.StatusForbidden, "Reference resources cannot be overwritten"
		case errors.Is(err, chat.ErrResourceDocumentIdentityMismatch):
			status, message = http.StatusForbidden, "resource identity mismatch"
		case errors.Is(err, chat.ErrResourceDocumentRevisionConflict):
			status, message = http.StatusConflict, "resource revision conflict"
		case errors.Is(err, chat.ErrChatNotFound), errors.Is(err, os.ErrNotExist):
			status, message = http.StatusNotFound, "resource not found"
		case errors.Is(err, os.ErrPermission):
			status, message = http.StatusForbidden, "resource access denied"
		}
		writeJSON(w, status, api.Failure(status, message))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}

type strictJSONDecoder interface {
	Decode(value any) error
}

func ensureJSONEOF(decoder strictJSONDecoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else {
		return err
	}
}
