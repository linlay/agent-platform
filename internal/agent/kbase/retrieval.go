package kbase

import "sort"

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

func retrievalCandidateLimit(req RetrievalRequest) int {
	req = normalizedRetrievalRequest(req)
	requested := req.Offset + req.Limit
	limit := req.CandidateMultiplier * requested
	if limit < req.CandidateFloor {
		limit = req.CandidateFloor
	}
	if limit > req.CandidateMax {
		limit = req.CandidateMax
	}
	return limit
}

type rankedChunk struct {
	Chunk chunkRecord
	Score float64
}

func fuseWeightedRRF(vector, fts []rankedChunk, req RetrievalRequest) RetrievalResponse {
	req = normalizedRetrievalRequest(req)
	type fused struct {
		chunk      chunkRecord
		vectorRank int
		ftsRank    int
	}
	byID := map[string]*fused{}
	for index, candidate := range vector {
		entry := byID[candidate.Chunk.ID]
		if entry == nil {
			entry = &fused{chunk: candidate.Chunk}
			byID[candidate.Chunk.ID] = entry
		}
		entry.vectorRank = index + 1
	}
	for index, candidate := range fts {
		entry := byID[candidate.Chunk.ID]
		if entry == nil {
			entry = &fused{chunk: candidate.Chunk}
			byID[candidate.Chunk.ID] = entry
		}
		entry.ftsRank = index + 1
	}

	matches := make([]RetrievalMatch, 0, len(byID))
	maxScore := 1.0 / float64(req.RRFK+1)
	for _, entry := range byID {
		raw := 0.0
		matchType := "hybrid"
		if entry.vectorRank > 0 {
			raw += req.VectorWeight / float64(req.RRFK+entry.vectorRank)
		}
		if entry.ftsRank > 0 {
			raw += req.FTSWeight / float64(req.RRFK+entry.ftsRank)
		}
		if entry.vectorRank == 0 {
			matchType = "fts"
		} else if entry.ftsRank == 0 {
			matchType = "vector"
		}
		score := raw / maxScore
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		matches = append(matches, RetrievalMatch{Chunk: entry.chunk, Score: score, MatchType: matchType})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Chunk.Path != matches[j].Chunk.Path {
			return matches[i].Chunk.Path < matches[j].Chunk.Path
		}
		if matches[i].Chunk.StartLine != matches[j].Chunk.StartLine {
			return matches[i].Chunk.StartLine < matches[j].Chunk.StartLine
		}
		return matches[i].Chunk.ID < matches[j].Chunk.ID
	})
	matchCount := len(matches)
	start := minInt(req.Offset, len(matches))
	end := minInt(start+req.Limit, len(matches))
	page := append([]RetrievalMatch(nil), matches[start:end]...)
	return RetrievalResponse{
		Matches:    page,
		MatchCount: matchCount,
		Truncated:  end < len(matches) || len(vector) >= req.CandidateMax || len(fts) >= req.CandidateMax,
		VectorHits: len(vector),
		FTSHits:    len(fts),
	}
}
