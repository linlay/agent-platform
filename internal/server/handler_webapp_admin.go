package server

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

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
	if path == "/sessions/revoke" && r.Method == http.MethodPost && s.webAppGateway != nil {
		s.webAppGateway.RevokeSessions()
		webAppResponse(w, map[string]bool{"revoked": true}, nil)
		return
	}
	if len(parts) == 2 && parts[0] != "" {
		id := parts[0]
		switch parts[1] {
		case "start", "stop", "restart":
			if r.Method != http.MethodPost {
				break
			}
			var input webapp.StateRequest
			if err := decodeWebAppRequest(r, &input); err != nil {
				webAppResponse(w, nil, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
			defer cancel()
			var value webapp.Entry
			var err error
			if parts[1] == "start" {
				value, err = s.deps.WebApps.Start(ctx, id, input)
			} else {
				value, err = s.deps.WebApps.Stop(ctx, id, input)
				if err == nil && parts[1] == "restart" {
					value, err = s.deps.WebApps.Start(ctx, id, webapp.StateRequest{ExpectedRevision: value.Revision, ExpectedStateRevision: value.StateRevision})
				}
			}
			webAppResponse(w, value, err)
			return
		case "launch-ticket":
			if r.Method != http.MethodPost || s.webAppGateway == nil {
				break
			}
			var input struct {
				ReturnPath string `json:"returnPath"`
			}
			if err := decodeWebAppRequest(r, &input); err != nil {
				webAppResponse(w, nil, err)
				return
			}
			value, err := s.webAppGateway.Launch(PrincipalFromContext(r.Context()).Subject, id, input.ReturnPath)
			webAppResponse(w, value, err)
			return
		case "user-config":
			if r.Method != http.MethodPut {
				break
			}
			var input webapp.UserConfigRequest
			if err := decodeWebAppRequest(r, &input); err != nil {
				webAppResponse(w, nil, err)
				return
			}
			value, err := s.deps.WebApps.SaveUserConfig(id, input)
			webAppResponse(w, value, err)
			return
		case "policy":
			if r.Method != http.MethodPut {
				break
			}
			var input webapp.PolicyRequest
			if err := decodeWebAppRequest(r, &input); err != nil {
				webAppResponse(w, nil, err)
				return
			}
			value, err := s.deps.WebApps.SavePolicy(id, input)
			webAppResponse(w, value, err)
			return
		}
	}
	if len(parts) == 1 && parts[0] != "" {
		if r.Method == http.MethodGet {
			value, err := s.deps.WebApps.Configuration(parts[0])
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
