package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"time"

	"agent-platform/internal/connector"
)

func (s *Server) handleConnectorIcon(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !connector.ValidID(id) {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_connector", "invalid connector id"))
		return
	}
	pkg, err := s.connectorSources().Load(id)
	var icon connector.IconAsset
	if err == nil {
		icon, err = pkg.ReadIcon()
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(status, "connector_icon_unavailable", "connector icon is unavailable"))
		return
	}
	w.Header().Set("Content-Type", icon.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", `"`+icon.SHA256+`"`)
	http.ServeContent(w, r, "icon", time.Time{}, bytes.NewReader(icon.Data))
}
