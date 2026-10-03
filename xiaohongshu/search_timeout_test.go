package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSearchContextDefaultAndExplicitWindow(t *testing.T) {
	for _, seconds := range []int{25, 30, 45} {
		start := time.Now()
		ctx, cancel, err := searchContext(context.Background(), time.Duration(seconds)*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		deadline, ok := ctx.Deadline()
		cancel()
		if !ok || deadline.Before(start.Add(time.Duration(seconds)*time.Second)) || deadline.After(time.Now().Add(time.Duration(seconds)*time.Second)) {
			t.Fatalf("incorrect %ds deadline", seconds)
		}
	}
	if DefaultSearchTimeout != 25*time.Second {
		t.Fatal("legacy searches must retain 25 seconds")
	}
}

func TestSearchTimeoutInvalidBeforeBrowserAccess(t *testing.T) {
	action := NewSearchAction(nil)
	for _, timeout := range []time.Duration{-time.Second, 0, 24 * time.Second, 46 * time.Second, 25500 * time.Millisecond} {
		if _, err := action.SearchWithTimeout(context.Background(), "fixture", timeout); err == nil {
			t.Fatalf("invalid duration accepted: %s", timeout)
		}
	}
}

func TestSearchTimeoutRespectsShorterParent(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer parentCancel()
	ctx, cancel, err := searchContext(parent, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("search extended parent deadline")
	}
	navigations := 0
	_, err = waitSearchFeeds(ctx, func() error { navigations++; return nil }, func() (searchFeedState, error) { return searchFeedState{}, nil })
	if !errors.Is(err, context.DeadlineExceeded) || navigations != 1 {
		t.Fatalf("deadline failed: err=%v navigations=%d", err, navigations)
	}
}

func TestLegacySearchCancellationBeforeBrowserAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSearchAction(nil).Search(ctx, "fixture")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("legacy cancellation lost: %v", err)
	}
}
