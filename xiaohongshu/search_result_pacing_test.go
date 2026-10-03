package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

type resultPacingProvider struct{ calls int }

func (p *resultPacingProvider) Timing() humanize.TimingProfile {
	p.calls++
	return humanize.TimingProfile{humanize.AfterNavigate: {
		Min: time.Second, Max: time.Second,
	}}
}

func TestUnfilteredReadyResultsDoNotSpendDeadlineOnInteractionPacing(t *testing.T) {
	provider := &resultPacingProvider{}
	humanize.SetProvider(provider)
	defer humanize.SetProvider(humanize.DefaultProvider{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reads := 0
	feeds, err := waitSearchFeeds(ctx, func() error { return nil }, func() (searchFeedState, error) {
		reads++
		return searchFeedState{Ready: true, Feeds: []Feed{{ID: "fixture-note", ModelType: "note"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := collectFilters([]FilterOption{{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSearchFilterInteraction(ctx, pending); err != nil {
		t.Fatal("already read unfiltered result was lost to optional waiting", err)
	}
	result := onlyNotes(feeds)
	if provider.calls != 0 || reads != 1 || len(result) != 1 || result[0].ID != "fixture-note" {
		t.Fatal("unfiltered read must return the original result without pacing or another read")
	}
}

func TestFilteredResultsKeepPacingAndOriginalDeadline(t *testing.T) {
	provider := &resultPacingProvider{}
	humanize.SetProvider(provider)
	defer humanize.SetProvider(humanize.DefaultProvider{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	pending, err := collectFilters([]FilterOption{{NoteType: "图文"}})
	if err != nil {
		t.Fatal(err)
	}
	err = prepareSearchFilterInteraction(ctx, pending)
	if !errors.Is(err, context.DeadlineExceeded) || provider.calls != 1 {
		t.Fatal("a required filter interaction must keep pacing and respect its original deadline", err)
	}
}

func TestUnfilteredResultsDoNotBypassActualCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareSearchFilterInteraction(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("removing optional pacing must not ignore cancellation", err)
	}
}
