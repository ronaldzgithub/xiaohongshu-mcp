package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestBindPublishIdentityFromHuaxiaobaoHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/publish", nil)
	c.Request.Header.Set("X-Request-ID", "request-1")
	c.Request.Header.Set("Idempotency-Key", "key-1")
	req := &PublishRequest{}

	require.NoError(t, bindPublishIdentity(c, req))
	assert.Equal(t, "request-1", req.RequestID)
	assert.Equal(t, "key-1", req.IdempotencyKey)
}

func TestBindPublishIdentityRejectsHeaderBodyMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/publish", nil)
	c.Request.Header.Set("X-Request-ID", "header-request")
	req := &PublishRequest{RequestID: "body-request"}

	err := bindPublishIdentity(c, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不一致")
}

func TestPublishHandlerReturnsTypedUnknownWithoutRetrying(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service := NewXiaohongshuService()
	service.processImage = func(paths []string) ([]string, error) { return paths, nil }
	called := 0
	service.publishImage = func(context.Context, xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
		called++
		return nil, xiaohongshu.ErrPublishResultUnverifiable
	}
	server := &AppServer{xiaohongshuService: service}
	body := []byte(`{"request_id":"request-1","idempotency_key":"key-1","title":"标题","content":"正文","images":["image.png"]}`)

	for range 2 {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/publish", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		server.publishHandler(c)

		assert.Equal(t, http.StatusAccepted, recorder.Code)
		var response ErrorResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "PUBLISH_RESULT_UNKNOWN", response.Code)
		assert.Contains(t, recorder.Body.String(), `"request_id":"request-1"`)
		assert.Contains(t, recorder.Body.String(), `"status":"UNKNOWN"`)
	}
	assert.Equal(t, 1, called)
}

// respondError 是全仓库共用的错误出口。分级只是一行 if，改回去编译和其他单测
// 都不会报错，但鉴权开启后被扫描器打的 401 会重新刷满 ERROR。
func TestRespondErrorLogLevel(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantLevel  logrus.Level
	}{
		{name: "401 记为 warning", statusCode: http.StatusUnauthorized, wantLevel: logrus.WarnLevel},
		{name: "400 记为 warning", statusCode: http.StatusBadRequest, wantLevel: logrus.WarnLevel},
		{name: "500 记为 error", statusCode: http.StatusInternalServerError, wantLevel: logrus.ErrorLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook := logrustest.NewGlobal()
			defer hook.Reset()

			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/login/status", nil)

			respondError(c, tt.statusCode, "CODE", "消息", nil)

			require.Len(t, hook.Entries, 1)
			assert.Equal(t, tt.wantLevel, hook.LastEntry().Level)
		})
	}
}
