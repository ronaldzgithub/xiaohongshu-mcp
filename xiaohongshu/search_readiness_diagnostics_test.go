package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func completedEmptyState() searchFeedState {
	return searchFeedState{HasInitialState: true, HasSearch: true, HasFeeds: true,
		FeedType: "array", FeedCount: 0, Feeds: []Feed{},
		Readiness: searchReadinessEvidence{Authenticated: true, QueryMatches: true,
			StoreSuccess: true, HasMoreKnown: true},
		Network: &searchNetworkDiagnostics{requests: map[proto.NetworkRequestID]searchNetworkRequest{
			"private-request": {HTTPStatus: 200, Completed: true},
		}},
	}
}

func TestSearchReadinessClassRequiresExplicitEmptyEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*searchFeedState)
		want   string
	}{
		{"explicit", func(s *searchFeedState) {}, "explicit_empty_observed"},
		{"initial-array", func(s *searchFeedState) { s.Readiness.StoreSuccess = false }, "unresolved"},
		{"unknown-has-more", func(s *searchFeedState) { s.Readiness.HasMoreKnown = false }, "unresolved"},
		{"more-results", func(s *searchFeedState) { s.Readiness.HasMore = true }, "unresolved"},
		{"different-query", func(s *searchFeedState) { s.Readiness.QueryMatches = false }, "unresolved"},
		{"no-observed-response", func(s *searchFeedState) { s.Network = nil }, "unresolved"},
		{"pending-response", func(s *searchFeedState) { s.Network.requests["pending"] = searchNetworkRequest{HTTPStatus: 200} }, "loading_observed"},
		{"login", func(s *searchFeedState) { s.Readiness.LoginVisible = true }, "login_required_observed"},
		{"challenge", func(s *searchFeedState) { s.Readiness.ChallengeVisible = true }, "challenge_observed"},
		{"store-error", func(s *searchFeedState) { s.Readiness.StoreError = true }, "store_error_observed"},
		{"store-loading", func(s *searchFeedState) { s.Readiness.StoreSuccess = false; s.Readiness.StoreLoading = true }, "loading_observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := completedEmptyState()
			tc.change(&s)
			if got := searchReadinessFields(s)["readiness_class"]; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestSearchReadinessCountsPendingScriptsWithoutPayloads(t *testing.T) {
	d := &searchNetworkDiagnostics{scripts: map[proto.NetworkRequestID]searchNetworkRequest{
		"secret-a": {Origin: "private-origin", Path: "/token?private", HTTPStatus: 200},
		"secret-b": {HTTPStatus: 200, Completed: true},
		"secret-c": {HTTPStatus: 403},
		"secret-d": {Failed: true},
	}}
	s := searchFeedState{HasInitialState: true, HasSearch: true, Network: d}
	fields := searchReadinessFields(s)
	if fields["startup_scripts_pending"] != 1 || fields["startup_scripts_completed"] != 1 || fields["startup_scripts_failed"] != 2 || fields["startup_scripts_observed"] != 4 {
		t.Fatalf("wrong counts: %+v", fields)
	}
	if fields["readiness_class"] != "startup_script_failed" {
		t.Fatal(fields)
	}
	delete(d.scripts, "secret-c")
	delete(d.scripts, "secret-d")
	if searchReadinessFields(s)["readiness_class"] != "startup_script_pending" {
		t.Fatal("pending is not success")
	}
	encoded, _ := json.Marshal(fields)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret") {
		t.Fatal("payload leaked")
	}
}

func TestSearchReadinessSamplingDoesNotPromoteEmptyToSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	state := completedEmptyState()
	state.Ready = true
	_, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) { return state, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("diagnostic must preserve success rule: %v", err)
	}
}

func TestSearchReadinessLogUsesOnlyFixedScalars(t *testing.T) {
	hook := failureLogHook(t)
	state := completedEmptyState()
	searchNotReadyError(context.DeadlineExceeded, state)
	entry := hook.LastEntry()
	fields := searchReadinessFields(state)
	for key, value := range fields {
		if entry.Data[key] != value {
			t.Fatalf("missing field %s", key)
		}
		switch value.(type) {
		case bool, int:
		case string:
			if key != "readiness_class" {
				t.Fatal("unexpected string")
			}
		default:
			t.Fatalf("unsafe type %T", value)
		}
	}
}
