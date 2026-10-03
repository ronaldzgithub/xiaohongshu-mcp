package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDetailSnapshotTimeoutContract(t *testing.T) {
	for _, seconds := range []string{"1", "15", "25"} {
		duration, err := detailSnapshotTimeout(FeedDetailRequest{TimeoutSeconds: json.RawMessage(seconds)})
		if err != nil || duration < time.Second || duration > 25*time.Second {
			t.Fatal(seconds, err)
		}
	}
	for _, seconds := range []string{"0", "26", "null", "true", "1.5", `"15"`, ""} {
		if _, err := detailSnapshotTimeout(FeedDetailRequest{TimeoutSeconds: json.RawMessage(seconds)}); err == nil {
			t.Fatal("accepted invalid duration", seconds)
		}
	}
	for _, req := range []FeedDetailRequest{
		{TimeoutSeconds: json.RawMessage("15"), LoadAllComments: true},
		{TimeoutSeconds: json.RawMessage("15"), CommentConfig: &CommentLoadConfig{}},
	} {
		if _, err := detailSnapshotTimeout(req); err == nil {
			t.Fatal("bounded snapshot must not load comments")
		}
	}
}

func TestDetailSnapshotLifecycleCannotDelayDeadlineEnvelope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	release, stopped := make(chan struct{}), make(chan struct{})
	started := time.Now()
	_, err := boundedDetailSnapshot(ctx, func() (*FeedDetailResponse, error) {
		<-release
		// Mirrors the post-creation guard: no page or navigation after cancellation.
		if ctx.Err() == nil {
			t.Error("expected canceled owner")
		}
		close(stopped)
		return nil, ctx.Err()
	})
	close(release)
	<-stopped
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("lifecycle delayed handler", err)
	}
}

func TestDetailSnapshotLifecyclePanicIsFixedError(t *testing.T) {
	_, err := boundedDetailSnapshot(context.Background(), func() (*FeedDetailResponse, error) { panic("secret fixture must not leak") })
	if err == nil || err.Error() != "detail snapshot browser failure" {
		t.Fatal("panic must become a sanitized failure")
	}
}
