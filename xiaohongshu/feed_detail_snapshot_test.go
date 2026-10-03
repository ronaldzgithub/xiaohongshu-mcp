package xiaohongshu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const snapshotFixture = `{"note":{"noteId":"fixture","title":"Title","desc":"","type":"normal"},"comments":{}}`

func TestDetailSnapshotReadsTargetWithoutWholePageStability(t *testing.T) {
	navigations, reads := 0, 0
	result, err := readDetailSnapshot(context.Background(), "fixture", func() error { navigations++; return nil }, func() (string, error) { reads++; return snapshotFixture, nil })
	if err != nil || result == nil || navigations != 1 || reads != 1 {
		t.Fatal("expected one exact snapshot", err)
	}
	if !strings.Contains(detailSnapshotJS, "noteDetailMap?.[id]") ||
		strings.Contains(detailSnapshotJS, "WaitDOM") || strings.Contains(detailSnapshotJS, "fetch(") {
		t.Fatal("readiness must use requested object")
	}
}

func TestDetailSnapshotCancelsAndDoesNotNavigateAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	navigations, reads := 0, 0
	_, err := readDetailSnapshot(ctx, "fixture", func() error { navigations++; return nil }, func() (string, error) { reads++; return "", nil })
	if !errors.Is(err, context.DeadlineExceeded) || navigations != 1 || reads != 1 {
		t.Fatal("deadline must stop the original read", err, navigations, reads)
	}
	_, err = readDetailSnapshot(ctx, "fixture", func() error { t.Fatal("navigation after deadline"); return nil }, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestDetailSnapshotErrorsAreReturnedWithoutPanicOrRetry(t *testing.T) {
	sentinel := errors.New("fixture failure")
	for _, stage := range []string{"navigate", "read"} {
		t.Run(stage, func(t *testing.T) {
			reads := 0
			_, err := readDetailSnapshot(context.Background(), "fixture", func() error {
				if stage == "navigate" {
					return sentinel
				}
				return nil
			}, func() (string, error) { reads++; return "", sentinel })
			if !errors.Is(err, sentinel) || reads > 1 {
				t.Fatal(err, reads)
			}
		})
	}
}

func TestDetailSnapshotRejectsWrongOrIncompleteObject(t *testing.T) {
	for _, raw := range []string{`{}`, `not JSON`, strings.Replace(snapshotFixture, "fixture", "other", 1), strings.Replace(snapshotFixture, `"desc":"",`, "", 1), strings.Replace(snapshotFixture, `"comments":{}`, `"comments":[]`, 1), strings.Replace(snapshotFixture, `"comments":{}`, `"comments":null`, 1)} {
		if _, err := decodeDetailSnapshot(raw, "fixture"); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}

func TestDetailSnapshotDoesNotAcceptLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := readDetailSnapshot(ctx, "fixture", func() error { return nil }, func() (string, error) { cancel(); return snapshotFixture, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("late response accepted", err)
	}
}
