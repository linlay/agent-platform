package server

import (
	"io"
	"mime"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/webapp"
)

func (s *Server) webAppOwner(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	p := PrincipalFromContext(r.Context())
	owners := s.deps.Config.Auth.AllowedSubjects
	if p == nil || p.Subject == "" || len(owners) != 1 || p.Subject != owners[0] {
		writeJSON(w, 403, api.Failure(403, "webapp_owner_required"))
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != s.deps.Config.WebApps.PlatformOrigin || r.Method != "GET" && r.Header.Get("Authorization") == "" && origin != s.deps.Config.WebApps.PlatformOrigin {
		writeJSON(w, 403, api.Failure(403, "webapp_origin_not_allowed"))
		return false
	}
	if s.deps.WebApps == nil {
		writeJSON(w, 503, api.Failure(503, "webapp_host_unavailable"))
		return false
	}
	return true
}
func webAppResponse(w http.ResponseWriter, value any, err error) {
	if err != nil {
		code, status := webapp.ErrorCode(err)
		writeJSON(w, status, api.Failure(status, code, map[string]any{"code": code, "status": status, "retryable": false}))
		return
	}
	writeJSON(w, 200, api.Success(value))
}
func decodeWebAppRequest(r *http.Request, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return &webapp.Error{Code: "webapp_invalid_request", Status: 400}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return &webapp.Error{Code: "webapp_invalid_request", Status: 400}
	}
	return webapp.DecodeRequest(raw, target)
}
func (s *Server) handleWebAppAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.webAppOwner(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/webapps")
	if path == "" && r.Method == http.MethodGet {
		webAppResponse(w, map[string]any{"items": s.deps.WebApps.List()}, nil)
		return
	}
	switch path {
	case "/uploads":
		if r.Method != http.MethodPost {
			break
		}
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media != "application/zip" && media != "application/octet-stream" {
			webAppResponse(w, nil, &webapp.Error{Code: "webapp_archive_content_type_invalid", Status: 415})
			return
		}
		value, err := s.deps.WebApps.Upload(r.Context(), r.Body)
		webAppResponse(w, value, err)
		return
	case "/preflight":
		if r.Method != http.MethodPost {
			break
		}
		var input struct {
			UploadID string `json:"uploadId"`
		}
		if err := decodeWebAppRequest(r, &input); err != nil {
			webAppResponse(w, nil, err)
			return
		}
		value, err := s.deps.WebApps.Preflight(r.Context(), input.UploadID)
		webAppResponse(w, value, err)
		return
	case "/install":
		if r.Method != http.MethodPost {
			break
		}
		var input webapp.InstallRequest
		if err := decodeWebAppRequest(r, &input); err != nil {
			webAppResponse(w, nil, err)
			return
		}
		value, err := s.deps.WebApps.Install(r.Context(), input)
		webAppResponse(w, value, err)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		if r.Method == http.MethodGet {
			value, err := s.deps.WebApps.Get(parts[0])
			webAppResponse(w, value, err)
			return
		}
		if r.Method == http.MethodDelete {
			var input struct {
				ExpectedRevision string `json:"expectedRevision"`
				PurgeData        bool   `json:"purgeData"`
			}
			if err := decodeWebAppRequest(r, &input); err != nil {
				webAppResponse(w, nil, err)
				return
			}
			err := s.deps.WebApps.Uninstall(r.Context(), parts[0], input.ExpectedRevision, input.PurgeData)
			webAppResponse(w, map[string]bool{"removed": err == nil}, err)
			return
		}
	}
	writeJSON(w, 404, api.Failure(404, "webapp_route_not_found"))
}
