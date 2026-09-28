package server

import (
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/i18n"
	"agent-platform/internal/runtime/controlscope"
)

func (s *Server) handleBTW(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(controlscope.WithContext(r.Context(), httpControlScope(r.Context(), "btw")))

	var input api.BTWRequest
	if err := decodeJSON(r, &input); err != nil {
		writeStatusError(w, btwStatusError(http.StatusBadRequest, "invalid_btw_request", "invalid request body"))
		return
	}
	cmd := trustedQueryCommand(r.Context(), api.QueryRequest{RequestID: input.RequestID, RunID: input.RunID, ChatID: input.ChatID, Message: input.Message, References: input.References, Params: input.Params, Scene: input.Scene, Stream: input.Stream, IncludeUsage: input.IncludeUsage, IncludeFullText: input.IncludeFullText, AccessLevel: input.AccessLevel, Model: input.Model})
	cmd.SideQuery = true
	cmd.SideQueryID = input.BTWID
	cmd.Locale = requestLocale(r, i18n.DefaultLocale)
	cmd.ResourceBaseURL = requestBaseURL(r)
	s.writeRuntimeQueryResponse(w, r.Context(), cmd)

}

func btwStatusError(status int, code string, message string) *statusError {
	return &statusError{
		Status:  status,
		Code:    code,
		Message: message,
		Data: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
}
