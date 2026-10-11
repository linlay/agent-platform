package server

import (
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/knowledge"
)

type knowledgeSourceReader interface {
	ReadBound(string, string, knowledge.ReadOptions) (knowledge.ReadResult, error)
	OpenBoundSource(string, string, string) (*os.File, error)
}

func (s *Server) handleKnowledgeSource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, 405, api.Failure(405, "method not allowed"))
		return
	}
	principal := PrincipalFromContext(r.Context())
	if principal == nil || strings.TrimSpace(principal.Subject) == "" {
		writeAuthError(w)
		return
	}
	var req struct {
		ChatID   string `json:"chatId"`
		SourceID string `json:"sourceId"`
		Offset   int    `json:"offset,omitempty"`
		Limit    int    `json:"limit,omitempty"`
	}
	if !decodeBoundedRequest(w, r, &req) {
		return
	}
	if s.deps.Chats == nil {
		writeAuthError(w)
		return
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if err != nil || summary == nil || !queryPrincipalCanReferenceChat(r.Context(), *summary) {
		writeJSON(w, 403, api.Failure(403, "chat access denied"))
		return
	}
	source, err := s.chatResources.PublishedKnowledgeSource(req.ChatID, req.SourceID)
	if err != nil {
		writeJSON(w, 404, api.Failure(404, err.Error()))
		return
	}
	reader, ok := s.deps.KBase.(knowledgeSourceReader)
	if !ok {
		writeJSON(w, 503, api.Failure(503, "knowledge reader unavailable"))
		return
	}
	p := strings.TrimPrefix(source.ID, "kbase:"+source.LibraryID+"/")
	if r.URL.Path == "/api/chat/sources/file" {
		file, err := reader.OpenBoundSource(source.AgentKey, source.LibraryID, p)
		if err != nil {
			writeJSON(w, 409, api.Failure(409, err.Error()))
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeJSON(w, 409, api.Failure(409, "source unavailable"))
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)}))
		http.ServeContent(w, r, path.Base(p), info.ModTime(), file)
		return
	}
	result, err := reader.ReadBound(source.AgentKey, source.LibraryID, knowledge.ReadOptions{Path: p, Offset: req.Offset, Limit: req.Limit})
	if err != nil {
		writeJSON(w, 409, api.Failure(409, err.Error()))
		return
	}
	writeJSON(w, 200, api.Success(result))
}
