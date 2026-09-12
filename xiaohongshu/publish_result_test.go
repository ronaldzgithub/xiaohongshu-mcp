package xiaohongshu

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePublishedFeedURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantID    string
		wantToken string
	}{
		{
			name:      "公开笔记地址",
			url:       "https://www.xiaohongshu.com/explore/64F1A2B3C4D5E6F7A8B9C0D1?xsec_token=controlled-token_123&xsec_source=pc_feed",
			wantID:    "64f1a2b3c4d5e6f7a8b9c0d1",
			wantToken: "controlled-token_123",
		},
		{
			name:   "旧版公开笔记地址",
			url:    "https://www.xiaohongshu.com/discovery/item/64f1a2b3c4d5e6f7a8b9c0d1",
			wantID: "64f1a2b3c4d5e6f7a8b9c0d1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parsePublishedFeedURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, result.FeedID)
			assert.Equal(t, tt.wantToken, result.XsecToken)
			assert.NotContains(t, result.EvidenceURL, "xsec_token")
		})
	}
}

func TestParsePublishedFeedURLFailsClosed(t *testing.T) {
	tests := map[string]string{
		"错误域名":            "https://www.xiaohongshu.com.example/explore/64f1a2b3c4d5e6f7a8b9c0d1",
		"非 HTTPS":         "http://www.xiaohongshu.com/explore/64f1a2b3c4d5e6f7a8b9c0d1",
		"创作后台任意 ID":       "https://creator.xiaohongshu.com/publish/success?noteId=64f1a2b3c4d5e6f7a8b9c0d1",
		"只离开编辑页":          "https://creator.xiaohongshu.com/creator/home",
		"用标题猜测":           "https://www.xiaohongshu.com/search_result?keyword=title",
		"缺少 ID":           "https://www.xiaohongshu.com/explore/",
		"非法 ID":           "https://www.xiaohongshu.com/explore/not-a-feed-id",
		"多余路径":            "https://www.xiaohongshu.com/explore/64f1a2b3c4d5e6f7a8b9c0d1/edit",
		"重复 access value": "https://www.xiaohongshu.com/explore/64f1a2b3c4d5e6f7a8b9c0d1?xsec_token=one&xsec_token=two",
	}

	for name, rawURL := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := parsePublishedFeedURL(rawURL)
			assert.Nil(t, result)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrPublishResultUnverifiable))
		})
	}
}

func TestScheduledConfirmationWithoutStableFeedIDRemainsUnknown(t *testing.T) {
	result, err := parsePublishedFeedURL("https://creator.xiaohongshu.com/publish/success?scheduled=true")

	assert.Nil(t, result)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPublishResultUnverifiable)
}

func TestPublishActionRejectsSecondSubmission(t *testing.T) {
	action := &PublishAction{}

	require.NoError(t, action.beginSubmission())
	err := action.beginSubmission()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "禁止重复")
}
