package server

import (
	"encoding/json"
	"io"
	"net/http"
)

type requestError struct {
	Code   string
	Status int
}

func writeRequestError(w http.ResponseWriter, err *requestError) {
	writeJSON(w, err.Status, map[string]any{"code": err.Status, "msg": err.Code, "data": map[string]any{"errorCode": err.Code}})
}

func decodeBoundedRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		writeRequestError(w, &requestError{Code: "invalid_arguments", Status: 400})
		return false
	}
	return true
}
