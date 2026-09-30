package xiaohongshu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

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
			Feeds: []Feed{{ID: "sensitive-test-id DOM", XsecToken: "?xsec_token=sensitive-test-token"}}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "initial_state=true search=true feeds=false") {
		t.Fatalf("missing readiness deadline diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-test") || strings.Contains(err.Error(), "?") {
		t.Fatal("diagnostic includes result data or query")
	}
	entries := hook.AllEntries()
	if len(entries) != 1 || entries[0].Message != "search_data_not_ready" || len(entries[0].Data) != 5 {
		t.Fatalf("expected one bounded readiness log, got %d entries", len(entries))
	}
	fields := entries[0].Data
	if fields["origin"] != "https://www.xiaohongshu.com" || fields["path"] != "/search_result" ||
		fields["initial_state"] != true || fields["search"] != true || fields["feeds"] != false {
		t.Fatal("readiness log lacks exact safe diagnostics")
	}
	serialized, serializeErr := entries[0].String()
	if serializeErr != nil || strings.Contains(serialized, "sensitive-test") || strings.Contains(serialized, "?") {
		t.Fatal("readiness log includes result data or query")
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
