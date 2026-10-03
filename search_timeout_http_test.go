package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSearchHTTPTimeoutLegacyAndExplicitPayload(t *testing.T) {
	for _, item := range []struct {
		body string
		want time.Duration
	}{
		{`{"keyword":"fixture","filters":{"note_type":"不限"}}`, 25 * time.Second},
		{`{"keyword":"fixture","timeout_seconds":25}`, 25 * time.Second},
		{`{"keyword":"fixture","timeout_seconds":45}`, 45 * time.Second},
	} {
		var request SearchFeedsRequest
		if err := json.Unmarshal([]byte(item.body), &request); err != nil {
			t.Fatal(err)
		}
		got, err := searchRequestTimeout(request.TimeoutSeconds)
		if err != nil || got != item.want || request.Keyword != "fixture" {
			t.Fatalf("timeout=%s err=%v", got, err)
		}
	}
}

func TestSearchHTTPTimeoutInvalidBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// nil service 确保错误参数不会创建浏览器或触发导航。
	server := &AppServer{}
	for _, value := range []string{"0", "24", "46", "-1", "25.5", "45.0", "1e2", "null", "true", `"45"`, "{}", "[]", "922337203685477580799"} {
		t.Run(value, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/feeds/search", strings.NewReader(`{"keyword":"fixture","timeout_seconds":`+value+`}`))
			c.Request.Header.Set("Content-Type", "application/json")
			server.searchFeedsHandler(c)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"INVALID_REQUEST"`) {
				t.Fatalf("invalid timeout reached service: %d", recorder.Code)
			}
		})
	}
}

func TestSearchServiceTimeoutRejectsBeforeBrowser(t *testing.T) {
	service := &XiaohongshuService{}
	if _, err := service.SearchFeedsWithTimeout(context.Background(), "fixture", 46*time.Second); err == nil {
		t.Fatal("service accepted invalid timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.SearchFeedsWithTimeout(ctx, "fixture", 45*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("explicit context cancellation lost: %v", err)
	}
	if _, err := service.SearchFeeds(ctx, "fixture"); !errors.Is(err, context.Canceled) {
		t.Fatalf("legacy context cancellation lost: %v", err)
	}
}
