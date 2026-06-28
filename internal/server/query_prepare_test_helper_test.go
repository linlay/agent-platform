package server

import "net/http"

func prepareQueryForTest(s *Server, r *http.Request) (preparedQuery, error) {
	admission, err := s.prepareQueryAdmission(r, true)
	if err != nil {
		return preparedQuery{}, err
	}
	return s.completeQueryPreparation(r.Context(), admission, nil)
}
