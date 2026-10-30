package xiaohongshu

import (
	stderrors "errors"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotSubmittedMarksOnlyPreSubmitFailures(t *testing.T) {
	assert.Nil(t, NotSubmitted(nil))

	cause := stderrors.New("upload input missing")
	marked := NotSubmitted(cause)
	assert.ErrorIs(t, marked, ErrPublishNotSubmitted)
	assert.ErrorIs(t, marked, cause)
	assert.Same(t, marked, NotSubmitted(marked))

	wrapped := errors.Wrap(marked, "小红书发布失败")
	assert.ErrorIs(t, wrapped, ErrPublishNotSubmitted)

	unknown := errors.Wrap(ErrPublishResultUnverifiable, "点击发布后未取得 feed_id")
	assert.NotErrorIs(t, unknown, ErrPublishNotSubmitted)
}

func TestDetailSnapshotKeepsTopicTags(t *testing.T) {
	raw := `{"note":{"noteId":"64f1a2b3c4d5e6f7a8b9c0d1","title":"标题","desc":"正文 #复盘[话题]#",` +
		`"type":"normal","user":{"userId":"user-1"},"imageList":[{"width":1,"height":1}],` +
		`"tagList":[{"id":"t1","name":"复盘","type":"topic"}]},"comments":{}}`

	result, err := decodeDetailSnapshot(raw, "64f1a2b3c4d5e6f7a8b9c0d1")
	require.NoError(t, err)
	require.Len(t, result.Note.TagList, 1)
	assert.Equal(t, DetailTag{ID: "t1", Name: "复盘", Type: "topic"}, result.Note.TagList[0])
	assert.Len(t, result.Note.ImageList, 1)
}
