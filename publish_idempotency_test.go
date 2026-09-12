package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func validPublishRequest() *PublishRequest {
	return &PublishRequest{
		RequestID:      "request-1",
		IdempotencyKey: "key-1",
		Title:          "标题",
		Content:        "正文",
		Images:         []string{"image.png"},
	}
}

func fakePublishService(t *testing.T) (*XiaohongshuService, *int) {
	t.Helper()
	service := NewXiaohongshuService()
	service.processImage = func(paths []string) ([]string, error) { return paths, nil }
	called := 0
	service.publishImage = func(context.Context, xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
		called++
		return &xiaohongshu.PublishResult{
			FeedID:      "64f1a2b3c4d5e6f7a8b9c0d1",
			EvidenceURL: "https://www.xiaohongshu.com/explore/64f1a2b3c4d5e6f7a8b9c0d1",
		}, nil
	}
	return service, &called
}

func TestPublishContentReplaysSameRequestWithoutSecondExternalAttempt(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service, called := fakePublishService(t)
	req := validPublishRequest()

	first, err := service.PublishContent(context.Background(), req)
	require.NoError(t, err)
	second, err := service.PublishContent(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, 1, *called)
	assert.Equal(t, first, second)
	assert.Equal(t, "VERIFIED", second.Status)
	assert.Equal(t, req.RequestID, second.RequestID)
}

func TestPublishContentCachesUnknownAndNeverResubmits(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service := NewXiaohongshuService()
	service.processImage = func(paths []string) ([]string, error) { return paths, nil }
	called := 0
	service.publishImage = func(context.Context, xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
		called++
		return nil, xiaohongshu.ErrPublishResultUnverifiable
	}
	req := validPublishRequest()

	_, firstErr := service.PublishContent(context.Background(), req)
	_, secondErr := service.PublishContent(context.Background(), req)

	assert.ErrorIs(t, firstErr, xiaohongshu.ErrPublishResultUnverifiable)
	assert.ErrorIs(t, secondErr, xiaohongshu.ErrPublishResultUnverifiable)
	assert.Equal(t, 1, called)
}

func TestScheduledPublishIsNotReportedAsAlreadyPublished(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service, called := fakePublishService(t)
	req := validPublishRequest()
	req.ScheduleAt = time.Now().Add(2 * time.Hour).Format(time.RFC3339)

	response, err := service.PublishContent(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, 1, *called)
	assert.Equal(t, "SCHEDULED", response.Status)
	assert.Equal(t, req.ScheduleAt, response.ScheduledFor)
	assert.NotEmpty(t, response.FeedID)
}

func TestPublishAttemptRejectsConcurrentDuplicate(t *testing.T) {
	service := NewXiaohongshuService()

	owner, _, err := service.beginPublishAttempt("key-1", "sha256:request")
	require.True(t, owner)
	require.NoError(t, err)

	owner, response, err := service.beginPublishAttempt("key-1", "sha256:request")
	assert.False(t, owner)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, ErrPublishAttemptInFlight)
}

func TestPublishAttemptReplaysVerifiedResultWithoutNewExecution(t *testing.T) {
	service := NewXiaohongshuService()
	owner, _, err := service.beginPublishAttempt("key-1", "sha256:request")
	require.True(t, owner)
	require.NoError(t, err)
	service.finishPublishAttempt("key-1", &PublishResponse{
		RequestID: "request-1",
		FeedID:    "64f1a2b3c4d5e6f7a8b9c0d1",
		Status:    "VERIFIED",
	}, nil)

	owner, response, err := service.beginPublishAttempt("key-1", "sha256:request")
	assert.False(t, owner)
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, "64f1a2b3c4d5e6f7a8b9c0d1", response.FeedID)
}

func TestPublishAttemptRejectsChangedRequestForSameKey(t *testing.T) {
	service := NewXiaohongshuService()
	owner, _, err := service.beginPublishAttempt("key-1", "sha256:first")
	require.True(t, owner)
	require.NoError(t, err)

	owner, response, err := service.beginPublishAttempt("key-1", "sha256:changed")
	assert.False(t, owner)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, ErrPublishIdentityConflict)
}
