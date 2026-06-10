package server

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/api"
)

var submitIDCounter atomic.Uint64

func normalizeSubmitRequest(req api.SubmitRequest) api.SubmitRequest {
	req.SubmitID = strings.TrimSpace(req.SubmitID)
	if req.SubmitID == "" {
		req.SubmitID = newSubmitID()
	}
	return req
}

func newSubmitID() string {
	millis := strconv.FormatInt(time.Now().UnixMilli(), 36)
	seq := strconv.FormatUint(submitIDCounter.Add(1), 36)
	return "submit_" + millis + "_" + seq
}
