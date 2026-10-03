package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestSearchNetworkDiagnosticsAreScopedBoundedAndRedacted(t *testing.T) {
	diagnostics := &searchNetworkDiagnostics{}
	request := func(id, address string, kind proto.NetworkResourceType) {
		diagnostics.request(&proto.NetworkRequestWillBeSent{RequestID: proto.NetworkRequestID(id), Type: kind,
			Request: &proto.NetworkRequest{URL: address}})
	}
	request("unrelated", "https://example.com/search?private", proto.NetworkResourceTypeXHR)
	request("wrong-host", "https://xiaohongshu.com.example.com/search?private", proto.NetworkResourceTypeXHR)
	request("wrong-kind", "https://www.xiaohongshu.com/search", proto.NetworkResourceTypeDocument)
	request("wrong-path", "https://www.xiaohongshu.com/api/feed", proto.NetworkResourceTypeFetch)
	count, requests := diagnostics.snapshot()
	if count != 0 || len(requests) != 0 {
		t.Fatal("unrelated traffic must not be retained")
	}
	request("private-request-id", "https://user:private-password@edith.xiaohongshu.com/api/sns/web/v1/search/notes?keyword=private-keyword#private-fragment", proto.NetworkResourceTypeXHR)
	diagnostics.response(&proto.NetworkResponseReceived{RequestID: "private-request-id", Response: &proto.NetworkResponse{Status: 200}})
	diagnostics.finished(&proto.NetworkLoadingFinished{RequestID: "private-request-id"})
	request("failed", "https://edith.xiaohongshu.com/api/search?xsec_token=private-token", proto.NetworkResourceTypeFetch)
	diagnostics.failed(&proto.NetworkLoadingFailed{RequestID: "failed", Canceled: true, BlockedReason: "csp", ErrorText: "private-error-text"})
	request("unknown-reason", "https://edith.xiaohongshu.com/api/search", proto.NetworkResourceTypeFetch)
	diagnostics.failed(&proto.NetworkLoadingFailed{RequestID: "unknown-reason", BlockedReason: "private-reason"})
	for index := 0; index < 10; index++ {
		request(fmt.Sprintf("extra%d", index), "https://edith.xiaohongshu.com/api/search", proto.NetworkResourceTypeFetch)
	}
	count, requests = diagnostics.snapshot()
	if count != 13 || len(requests) != 8 || requests[0].HTTPStatus != 200 || !requests[0].Completed ||
		!requests[1].Failed || !requests[1].Canceled || requests[1].BlockedReason != "csp" || requests[2].BlockedReason != "other" {
		t.Fatalf("incorrect bounded request summary: count=%d requests=%+v", count, requests)
	}
	if requests[0].Origin != "https://edith.xiaohongshu.com" || requests[0].Path != "/api/sns/web/v1/search/notes" {
		t.Fatal("request address must contain only origin and path")
	}
	encoded, err := json.Marshal(requests)
	if err != nil || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "?") {
		t.Fatal("network diagnostics leaked request metadata")
	}
	requests[0].HTTPStatus = 500
	_, again := diagnostics.snapshot()
	if again[0].HTTPStatus != 200 {
		t.Fatal("snapshot must not share mutable request storage")
	}
}

func TestSearchNetworkListenerEndsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	stop := runSearchNetworkListener(cancel, func() { <-ctx.Done(); close(finished) })
	stopped := make(chan struct{})
	go func() { stop(); stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("listener stop blocked after cancellation")
	}
	select {
	case <-finished:
	default:
		t.Fatal("stop returned before listener exited")
	}
}

func TestSearchNetworkDiagnosticsJoinSingleTimeoutLog(t *testing.T) {
	oldHooks := logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
	defer logrus.StandardLogger().ReplaceHooks(oldHooks)
	hook := logrustest.NewGlobal()
	diagnostics := &searchNetworkDiagnostics{}
	searchNotReadyError(context.DeadlineExceeded, searchFeedState{Network: diagnostics})
	entries := hook.AllEntries()
	if len(entries) != 1 || entries[0].Data["search_request_count"] != 0 ||
		entries[0].Data["search_requests"] != "[]" || entries[0].Data["page_request_count"] != 0 ||
		entries[0].Data["network_enable_status"] != "not_checked" || entries[0].Data["search_startup_script_failures"] != "[]" {
		t.Fatal("no observed search request must be explicit in the single timeout log")
	}
}

func TestSearchNetworkEnableStatusNeverExposesCDPError(t *testing.T) {
	for _, item := range []struct {
		err  error
		want string
	}{
		{nil, "enabled"}, {fmt.Errorf("private: %w", context.DeadlineExceeded), "deadline"},
		{context.Canceled, "cancelled"}, {errors.New("private-token DOM browser error"), "enable_failed"},
	} {
		if got := searchNetworkEnableStatus(item.err); got != item.want {
			t.Fatalf("status=%s want=%s", got, item.want)
		}
	}
}

func TestSearchStartupScriptFailuresAreBoundedAndOnlyKnownPublicPaths(t *testing.T) {
	diagnostics := &searchNetworkDiagnostics{enableStatus: "enabled"}
	prefix := "https://fe-static.xhscdn.com/formula-static/xhs-pc-web/public/resource/js/"
	for index, address := range []string{
		prefix + "async/Search.6e28756e.js?private-token#private-fragment",
		prefix + "index.0280008a.js", prefix + "vendor.f7bfc54c.js",
		prefix + "async/private-token/Search.6e28756e.js",
		prefix + "async/private-token.12345678.js",
		"https://example.com/formula-static/xhs-pc-web/public/resource/js/async/Search.6e28756e.js",
	} {
		id := proto.NetworkRequestID(fmt.Sprintf("script-%d", index))
		diagnostics.request(&proto.NetworkRequestWillBeSent{RequestID: id, Type: proto.NetworkResourceTypeScript, Request: &proto.NetworkRequest{URL: address}})
		status := 403
		if index == 2 {
			status = 200
		}
		diagnostics.response(&proto.NetworkResponseReceived{RequestID: id, Response: &proto.NetworkResponse{Status: status}})
		if index == 1 {
			diagnostics.failed(&proto.NetworkLoadingFailed{RequestID: id, Canceled: true, ErrorText: "private-error"})
		}
	}
	status, total, failures := diagnostics.startupSnapshot()
	if status != "enabled" || total != 6 || len(failures) != 2 || failures[0].HTTPStatus != 403 || !failures[1].Canceled {
		t.Fatalf("unexpected startup summary: %s %d %+v", status, total, failures)
	}
	encoded, _ := json.Marshal(failures)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "?") {
		t.Fatal("unsafe script failure summary")
	}
	for index := 0; index < 50; index++ {
		id := proto.NetworkRequestID(fmt.Sprintf("repeated-script-%d", index))
		diagnostics.request(&proto.NetworkRequestWillBeSent{RequestID: id, Type: proto.NetworkResourceTypeScript, Request: &proto.NetworkRequest{URL: prefix + "async/Search.6e28756e.js"}})
		diagnostics.failed(&proto.NetworkLoadingFailed{RequestID: id})
	}
	_, total, failures = diagnostics.startupSnapshot()
	if total != 56 || len(failures) != 8 || len(diagnostics.scripts) > 32 {
		t.Fatal("startup diagnostics must remain bounded")
	}
}

func TestSearchReadinessWaitsForFeedsWithoutPageStability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	navigations, observations := 0, 0
	feeds, err := waitSearchFeeds(ctx, func() error {
		navigations++
		return nil
	}, func() (searchFeedState, error) {
		observations++
		if observations == 1 {
			return searchFeedState{HasInitialState: true}, nil
		}
		return searchFeedState{Ready: true, Feeds: []Feed{{ModelType: "note"}}}, nil
	})
	if err != nil || len(feeds) != 1 || navigations != 1 || observations != 2 {
		t.Fatalf("typed feeds: count=%d err=%v navigation=%d observations=%d", len(feeds), err, navigations, observations)
	}
}

func TestSearchReadinessInitialEmptyArrayIsNotCompletedSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	feeds, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) {
		return searchFeedState{Ready: true, Feeds: []Feed{}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || feeds != nil {
		t.Fatalf("initial empty array accepted as completed search: count=%d err=%v", len(feeds), err)
	}
	_, err = waitSearchFeeds(context.Background(), func() error { return nil }, func() (searchFeedState, error) {
		return searchFeedState{Ready: true}, nil
	})
	if err == nil {
		t.Fatal("missing array cannot become a successful empty result")
	}
}

func TestSearchReadinessDeadlineHasOnlySafePageDiagnostics(t *testing.T) {
	oldHooks := logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
	defer logrus.StandardLogger().ReplaceHooks(oldHooks)
	hook := logrustest.NewGlobal()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) {
		return searchFeedState{Origin: "https://www.xiaohongshu.com", Path: "/search_result",
			HasInitialState: true, HasSearch: true, HasFeeds: false,
			FeedType: "array", FeedCount: 0,
			SearchFields:   map[string]string{"feeds": "array", "loading": "boolean", "error": "string", "query": "string", "?sensitive-test": "string"},
			SearchBooleans: map[string]bool{"loading": true, "?sensitive-test": true},
			Feeds:          []Feed{{ID: "sensitive-test-id DOM", XsecToken: "?xsec_token=sensitive-test-token"}}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "initial_state=true search=true feeds=false") {
		t.Fatalf("missing readiness deadline diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-test") || strings.Contains(err.Error(), "?") {
		t.Fatal("diagnostic includes result data or query")
	}
	entries := hook.AllEntries()
	if len(entries) != 1 || entries[0].Message != "search_data_not_ready" || len(entries[0].Data) != 27 {
		t.Fatalf("expected one bounded readiness log, got %d entries", len(entries))
	}
	fields := entries[0].Data
	if fields["readiness_class"] != "unresolved" || fields["query_matches"] != false ||
		fields["startup_scripts_pending"] != 0 || fields["search_requests_completed_2xx"] != 0 {
		t.Fatal("new readiness evidence must remain bounded and cannot infer completion")
	}
	if fields["feeds_type"] != "array" || fields["feeds_count"] != 0 ||
		fields["search_fields"].(map[string]string)["error"] != "string" ||
		fields["search_booleans"].(map[string]bool)["loading"] != true {
		t.Fatal("readiness log lacks actual state types and boolean values")
	}
	if fields["origin"] != "https://www.xiaohongshu.com" || fields["path"] != "/search_result" ||
		fields["initial_state"] != true || fields["search"] != true || fields["feeds"] != false {
		t.Fatal("readiness log lacks exact safe diagnostics")
	}
	serialized, serializeErr := entries[0].String()
	if serializeErr != nil || strings.Contains(serialized, "sensitive-test") || strings.Contains(serialized, "?") {
		t.Fatal("readiness log includes result data or query")
	}
}

func TestSearchReadinessStateDiagnosticsAreBoundedAndNeverReadinessProof(t *testing.T) {
	state := searchFeedState{SearchFields: map[string]string{
		"error": "sensitive-test DOM ?token=secret", "finished": "boolean", "text": "string",
	}, SearchBooleans: map[string]bool{"finished": true, "text": true, "notObserved": true}}
	fields, flags := safeSearchStateFields(state)
	if fields["error"] != "unknown" || len(flags) != 1 || !flags["finished"] {
		t.Fatal("only observed booleans and fixed type names may be logged")
	}
	for index := 0; index < 100; index++ {
		state.SearchFields[fmt.Sprintf("field%d", index)] = "object"
	}
	fields, _ = safeSearchStateFields(state)
	if len(fields) != 64 {
		t.Fatalf("state fields must be bounded, got %d", len(fields))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) {
		return searchFeedState{FeedType: "array", FeedCount: 0, Feeds: []Feed{},
			SearchFields: map[string]string{"finished": "boolean"}, SearchBooleans: map[string]bool{"finished": true}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("an unverified completion field must not make empty feeds ready")
	}
}

func TestSearchReadinessNavigationAndObservationErrorsReturn(t *testing.T) {
	failure := errors.New("test failure with sensitive-test-id DOM and ?xsec_token=sensitive-test-token")
	observed := false
	_, err := waitSearchFeeds(context.Background(), func() error { return failure }, func() (searchFeedState, error) {
		observed = true
		return searchFeedState{}, nil
	})
	if observed || !errors.Is(err, failure) || !strings.Contains(err.Error(), "navigate search") {
		t.Fatalf("navigation error lost: observed=%v err=%v", observed, err)
	}
	_, err = waitSearchFeeds(context.Background(), func() error { return nil }, func() (searchFeedState, error) {
		return searchFeedState{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("observation error lost: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-test") || strings.Contains(err.Error(), "DOM") || strings.Contains(err.Error(), "?") {
		t.Fatal("browser exception leaked into public error")
	}
}

func TestSearchReadinessCancellationCannotBecomeSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	feeds, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) {
		cancel()
		return searchFeedState{Ready: true, Feeds: []Feed{}}, nil
	})
	if feeds != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read accepted: feeds=%v err=%v", feeds, err)
	}
}

func TestSearchReadinessNavigationUsesSameDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	observed := false
	_, err := waitSearchFeeds(ctx, func() error { <-ctx.Done(); return nil }, func() (searchFeedState, error) {
		observed = true
		return searchFeedState{Ready: true, Feeds: []Feed{}}, nil
	})
	if observed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("navigation deadline lost: observed=%v err=%v", observed, err)
	}
}

func TestSearchReadinessFilterRefreshUsesSameDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := waitFeedsChanged(ctx, "before", 15*time.Second, func() string { return "before" })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("filter wait escaped the search deadline: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	err = waitFeedsChanged(ctx, "before", 15*time.Second, func() string {
		cancel()
		return "after"
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled filter result accepted: %v", err)
	}
	if err := waitFeedsChanged(context.Background(), "before", time.Second, func() string { return "after" }); err != nil {
		t.Fatalf("changed filter result not accepted: %v", err)
	}
}
