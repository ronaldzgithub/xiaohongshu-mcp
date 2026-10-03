package xiaohongshu

// Fixed booleans only. The existing saved public index.0280008a.js module 57024
// sets state=loading on reset, success after setNotes, and error on API failure.
// No raw state, query, user identity, DOM text, headers or response body is logged.
type searchReadinessEvidence struct {
	DocumentComplete bool `json:"documentComplete"`
	LoginVisible     bool `json:"loginVisible"`
	ChallengeVisible bool `json:"challengeVisible"`
	Authenticated    bool `json:"authenticated"`
	Unauthenticated  bool `json:"unauthenticated"`
	StoreLoading     bool `json:"storeLoading"`
	StoreSuccess     bool `json:"storeSuccess"`
	StoreError       bool `json:"storeError"`
	HasMoreKnown     bool `json:"hasMoreKnown"`
	HasMore          bool `json:"hasMore"`
	QueryMatches     bool `json:"queryMatches"`
}

type searchResourceCounts struct {
	Observed, Pending, Completed, Failed int
}

func (d *searchNetworkDiagnostics) readinessCounts() (scripts searchResourceCounts, searchCompleted, searchPending int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, value := range d.scripts {
		scripts.Observed++
		if value.Failed || value.HTTPStatus >= 400 {
			scripts.Failed++
		} else if value.Completed {
			scripts.Completed++
		} else {
			scripts.Pending++
		}
	}
	for _, value := range d.requests {
		if value.Completed && !value.Failed && value.HTTPStatus >= 200 && value.HTTPStatus < 300 {
			searchCompleted++
		} else if !value.Completed && !value.Failed && value.HTTPStatus < 400 {
			searchPending++
		}
	}
	return
}

func searchReadinessFields(state searchFeedState) map[string]interface{} {
	r := state.Readiness
	var scripts searchResourceCounts
	var completed, pending int
	if state.Network != nil {
		scripts, completed, pending = state.Network.readinessCounts()
	}
	classification := "unresolved"
	switch {
	case r.ChallengeVisible:
		classification = "challenge_observed"
	case r.Unauthenticated || r.LoginVisible:
		classification = "login_required_observed"
	case r.StoreError:
		classification = "store_error_observed"
	case r.Authenticated && r.QueryMatches && r.StoreSuccess && r.HasMoreKnown && !r.HasMore &&
		state.HasFeeds && state.FeedType == "array" && state.FeedCount == 0 && completed > 0 && pending == 0:
		classification = "explicit_empty_observed"
	case scripts.Failed > 0:
		classification = "startup_script_failed"
	case scripts.Pending > 0:
		classification = "startup_script_pending"
	case r.StoreLoading || pending > 0:
		classification = "loading_observed"
	case !state.HasInitialState || !state.HasSearch:
		classification = "initializing"
	}
	return map[string]interface{}{
		"readiness_class":   classification,
		"document_complete": r.DocumentComplete,
		"login_visible":     r.LoginVisible, "challenge_visible": r.ChallengeVisible,
		"authenticated": r.Authenticated, "unauthenticated": r.Unauthenticated,
		"store_loading": r.StoreLoading, "store_success": r.StoreSuccess, "store_error": r.StoreError,
		"query_matches": r.QueryMatches, "has_more_known": r.HasMoreKnown, "has_more": r.HasMore,
		"startup_scripts_observed": scripts.Observed, "startup_scripts_pending": scripts.Pending,
		"startup_scripts_completed": scripts.Completed, "startup_scripts_failed": scripts.Failed,
		"search_requests_completed_2xx": completed, "search_requests_pending": pending,
	}
}
