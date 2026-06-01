package server

import (
	"errors"
	"net/http"

	"agent-platform/internal/api"
)

func (s *Server) handleQueryAvailability(w http.ResponseWriter, r *http.Request) {
	admission, err := s.prepareQueryAdmission(r, false)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.queryAvailability(admission)))
}

func (s *Server) queryAvailability(admission queryAdmission) api.QueryAvailabilityResponse {
	return availabilityOK(admission.req.AgentKey, admission.req.ChatID, admission.req.TeamID)
}

func (s *Server) tryAcquireQuery(admission queryAdmission) (queryReleaseFunc, api.QueryAvailabilityResponse) {
	return func() {}, s.queryAvailability(admission)
}

type queryReleaseFunc func()

func availabilityOK(agentKey string, chatID string, teamID string) api.QueryAvailabilityResponse {
	return api.QueryAvailabilityResponse{
		CanQuery: true,
		Code:     "ok",
		Message:  "ok",
		AgentKey: agentKey,
		ChatID:   chatID,
		TeamID:   teamID,
	}
}

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}
