package model

// Nil tokens mean unknown, not free. Partial usage can survive a failed attempt.
type Usage struct {
	Model             string `json:"model"`
	ModelRuns         int64  `json:"model_runs"`
	ModelRequests     int64  `json:"model_requests"`
	InputTokens       *int64 `json:"input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	Complete          bool   `json:"usage_complete"`
	SearchCalls       int64  `json:"search_calls"`
	SearchCacheHits   int64  `json:"search_cache_hits"`
	SearchCacheMisses int64  `json:"search_cache_misses"`
	SearchErrors      int64  `json:"search_errors"`
}

func (u *Usage) Valid() bool {
	if u == nil {
		return true
	}
	if len(u.Model) > 128 {
		return false
	}
	for _, n := range []int64{u.ModelRuns, u.ModelRequests, u.SearchCalls, u.SearchCacheHits, u.SearchCacheMisses, u.SearchErrors} {
		if n < 0 || n > 1000000 {
			return false
		}
	}
	for _, n := range []*int64{u.InputTokens, u.OutputTokens} {
		if n != nil && (*n < 0 || *n > 1000000000) {
			return false
		}
	}
	return !u.Complete || (u.InputTokens != nil && u.OutputTokens != nil)
}
