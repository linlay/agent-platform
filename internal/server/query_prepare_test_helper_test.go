package server

import (
	"net/http"

	"agent-platform/internal/i18n"
)

func prepareQueryForTest(s *Server, r *http.Request) (preparedQuery, error) {
	req, err := decodeQueryRequest(r)
	if err != nil {
		return preparedQuery{}, err
	}
	admission, err := s.prepareQueryAdmissionRequest(r.Context(), req, true, requestLocale(r, i18n.DefaultLocale), requestBaseURL(r))
	if err != nil {
		return preparedQuery{}, err
	}
	return s.completeQueryPreparation(r.Context(), admission, nil)
}
