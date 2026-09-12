package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestMCPPublishResultNeverExposesXsecToken(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")

	service := NewXiaohongshuService()
	service.processImage = func(images []string) ([]string, error) {
		return append([]string(nil), images...), nil
	}
	service.publishImage = func(
		context.Context,
		xiaohongshu.PublishImageContent,
	) (*xiaohongshu.PublishResult, error) {
		return &xiaohongshu.PublishResult{
			FeedID:      "0123456789abcdef01234567",
			XsecToken:   "must-never-enter-mcp-output",
			EvidenceURL: "https://www.xiaohongshu.com/explore/0123456789abcdef01234567",
		}, nil
	}

	server := &AppServer{xiaohongshuService: service}
	result := server.handlePublishContent(context.Background(), map[string]interface{}{
		"request_id":      "request:mcp-publish-security",
		"idempotency_key": "idem:mcp-publish-security",
		"title":           "安全测试",
		"content":         "不执行真实发布",
		"images":          []interface{}{`C:\sandbox\fixture.png`},
	})

	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text := result.Content[0].Text
	assert.NotContains(t, text, "must-never-enter-mcp-output")
	assert.NotContains(t, strings.ToLower(text), "xsec")
	assert.Contains(t, text, "feed_id=0123456789abcdef01234567")
}
