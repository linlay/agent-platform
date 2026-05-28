package server

import (
	"errors"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
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
	limit := catalog.EffectiveAgentKanbanConcurrency(admission.agentDef)
	if s.queryLimiter == nil {
		return availabilityOK(admission.req.AgentKey, admission.req.ChatID, admission.req.TeamID, limit, nil)
	}
	return s.queryLimiter.Snapshot(admission.req.AgentKey, admission.req.ChatID, admission.req.TeamID, limit)
}

func (s *Server) tryAcquireQuery(admission queryAdmission) (queryReleaseFunc, api.QueryAvailabilityResponse) {
	limit := catalog.EffectiveAgentKanbanConcurrency(admission.agentDef)
	if s.queryLimiter == nil {
		return func() {}, availabilityOK(admission.req.AgentKey, admission.req.ChatID, admission.req.TeamID, limit, nil)
	}
	return s.queryLimiter.TryAcquire(admission.req.AgentKey, admission.req.RunID, admission.req.ChatID, admission.req.TeamID, limit)
}

func queryAvailabilityFailure(availability api.QueryAvailabilityResponse) api.ApiResponse[map[string]any] {
	return api.ApiResponse[map[string]any]{
		Code: http.StatusTooManyRequests,
		Msg:  availability.Message,
		Data: queryAvailabilityDetails(availability),
	}
}

func queryAvailabilityDetails(availability api.QueryAvailabilityResponse) map[string]any {
	return map[string]any{
		"code":        availability.Code,
		"message":     availability.Message,
		"agentKey":    availability.AgentKey,
		"chatId":      availability.ChatID,
		"teamId":      availability.TeamID,
		"concurrency": availability.Concurrency,
		"activeCount": availability.ActiveCount,
		"activeRuns":  availability.ActiveRuns,
	}
}

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}
