package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func failureLogHook(t *testing.T) *logrustest.Hook {
	t.Helper()
	logger := logrus.StandardLogger()
	oldHooks := logger.ReplaceHooks(make(logrus.LevelHooks))
	oldOutput := logger.Out
	logger.SetOutput(io.Discard)
	t.Cleanup(func() { logger.ReplaceHooks(oldHooks); logger.SetOutput(oldOutput) })
	return logrustest.NewGlobal()
}

func TestSearchFailureDiagnosticsPreserveStageAndErrorClass(t *testing.T) {
	hook := failureLogHook(t)
	for _, c := range []struct {
		err   error
		point string
		class string
	}{
		{&searchStageError{"navigate search failed", context.DeadlineExceeded}, "navigate", "deadline"},
		{&searchStageError{"read search feeds failed", context.Canceled}, "read", "cancel"},
		{&searchStageError{"read search feeds failed", &searchStageError{"decode search feeds failed", errors.New("private-token")}}, "decode", "error"},
		{fmt.Errorf("private-query: %w", context.DeadlineExceeded), "stage", "deadline"},
		{errors.New("private-cookie and private-result"), "stage", "error"},
	} {
		hook.Reset()
		logSearchFailure("initial_results", c.err, nil)
		entry := hook.LastEntry()
		if len(hook.AllEntries()) != 1 || entry.Message != "search_operation_failed" ||
			entry.Data["stage"] != "initial_results" || entry.Data["failure_point"] != c.point || entry.Data["error_class"] != c.class ||
			entry.Data["network_observed"] != false {
			t.Fatal("failure classification is missing or incorrect")
		}
		encoded, _ := json.Marshal(entry.Data)
		if strings.Contains(string(encoded), "private") {
			t.Fatal("failure diagnostic leaked error data")
		}
	}
	logSearchFailure("private-arbitrary-stage", errors.New("private-error"), nil)
	if hook.LastEntry().Data["stage"] != "unknown" {
		t.Fatal("unknown stage must not be logged verbatim")
	}
	hook.Reset()
	logSearchFailure("initial_results", nil, nil)
	if len(hook.AllEntries()) != 0 {
		t.Fatal("successful operations must not be logged as failures")
	}
}

func TestSearchFailureDiagnosticsRetainOnlyBoundedNetworkMetadata(t *testing.T) {
	hook := failureLogHook(t)
	network := &searchNetworkDiagnostics{}
	network.request(&proto.NetworkRequestWillBeSent{RequestID: "private-request", Type: proto.NetworkResourceTypeXHR,
		Request: &proto.NetworkRequest{URL: "https://edith.xiaohongshu.com/api/search?keyword=private-keyword&xsec_token=private-token"}})
	network.failed(&proto.NetworkLoadingFailed{RequestID: "private-request", ErrorText: "private-network-message", Canceled: true})
	logSearchFailure("network_observer", context.DeadlineExceeded, network)
	data := hook.LastEntry().Data
	if data["search_request_count"] != 1 || data["network_enable_status"] != "not_checked" ||
		data["page_request_count"] != 1 || data["search_startup_script_failures"] != "[]" {
		t.Fatal("existing network counters were not preserved")
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "keyword") || strings.Contains(string(encoded), "?") {
		t.Fatal("failure diagnostic leaked request or error content")
	}
}

func TestSearchFailureActionExitLogsWithoutBrowserOrRetry(t *testing.T) {
	hook := failureLogHook(t)
	action := NewSearchAction(nil)
	_, err := action.SearchWithTimeout(context.Background(), "private-keyword", 46*time.Second)
	if err == nil || len(hook.AllEntries()) != 1 || hook.LastEntry().Data["stage"] != "context" {
		t.Fatal("invalid timeout must fail before browser access with a bounded diagnostic")
	}
	hook.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = action.SearchWithTimeout(ctx, "private-keyword", 45*time.Second)
	if !errors.Is(err, context.Canceled) || len(hook.AllEntries()) != 1 || hook.LastEntry().Data["error_class"] != "cancel" {
		t.Fatal("original cancellation must remain intact")
	}
	hook.Reset()
	_, err = action.SearchWithTimeout(context.Background(), "private-keyword", 45*time.Second, FilterOption{NoteType: "private-invalid-filter"})
	if err == nil || len(hook.AllEntries()) != 1 || hook.LastEntry().Data["stage"] != "filter_validation" {
		t.Fatal("invalid filter must fail before browser access with its own stage")
	}
	encoded, _ := json.Marshal(hook.LastEntry().Data)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("action exit logged query/filter data")
	}
}

func TestSearchNavigateFailureRemainsSingleAttemptAndClassifiable(t *testing.T) {
	hook := failureLogHook(t)
	navigations, observations := 0, 0
	_, err := waitSearchFeeds(context.Background(), func() error {
		navigations++
		return context.DeadlineExceeded
	}, func() (searchFeedState, error) {
		observations++
		return searchFeedState{}, nil
	})
	logSearchFailure("initial_results", err, &searchNetworkDiagnostics{})
	if navigations != 1 || observations != 0 || !errors.Is(err, context.DeadlineExceeded) ||
		hook.LastEntry().Data["failure_point"] != "navigate" {
		t.Fatal("navigation failure identity/cause or original single attempt changed")
	}
}
