package kbase

func normalizedRetrievalRequest(req RetrievalRequest) RetrievalRequest {
	req.PathPrefix = normalizeIndexedPath(req.PathPrefix)
	req.PathGlob = normalizeKBaseGlob(req.PathGlob)
	req.Type = normalizeKBaseExt(req.Type)
	if req.Limit <= 0 {
		req.Limit = 8
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.CandidateFloor < req.Limit {
		req.CandidateFloor = maxInt(30, req.Limit)
	}
	if req.CandidateMultiplier <= 0 {
		req.CandidateMultiplier = 4
	}
	if req.CandidateMax < req.CandidateFloor {
		req.CandidateMax = 500
	}
	if req.CandidateMax > 2000 {
		req.CandidateMax = 2000
	}
	if req.RRFK <= 0 {
		req.RRFK = 60
	}
	if req.VectorWeight < 0 {
		req.VectorWeight = 0
	}
	if req.FTSWeight < 0 {
		req.FTSWeight = 0
	}
	if req.VectorWeight+req.FTSWeight == 0 {
		req.VectorWeight = 0.7
		req.FTSWeight = 0.3
	}
	weightSum := req.VectorWeight + req.FTSWeight
	req.VectorWeight /= weightSum
	req.FTSWeight /= weightSum
	return req
}
