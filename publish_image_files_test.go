package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func testPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: shade, G: 10, B: 20, A: 255})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, canvas))
	return buffer.Bytes()
}

func imageFile(content []byte, mediaType string) PublishImageFile {
	sum := sha256.Sum256(content)
	return PublishImageFile{
		ContentBase64: base64.StdEncoding.EncodeToString(content),
		SHA256:        "sha256:" + hex.EncodeToString(sum[:]),
		MediaType:     mediaType,
	}
}

func TestMaterializePublishImageFilesVerifiesAndCleansUp(t *testing.T) {
	content := testPNG(t, 1)
	paths, cleanup, err := materializePublishImageFiles([]PublishImageFile{imageFile(content, "image/png")})
	require.NoError(t, err)
	require.Len(t, paths, 1)
	written, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, content, written)
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(paths[0])
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	cleanup()
	_, err = os.Stat(paths[0])
	assert.True(t, os.IsNotExist(err))
}

func TestMaterializePublishImageFilesRejectsUntrustedFacts(t *testing.T) {
	content := testPNG(t, 2)
	valid := imageFile(content, "image/png")

	wrongDigest := valid
	wrongDigest.SHA256 = "sha256:" + hex.EncodeToString(make([]byte, 32))
	wrongType := valid
	wrongType.MediaType = "image/jpeg"
	badBase64 := valid
	badBase64.ContentBase64 = "not base64!"
	prefixless := valid
	prefixless.SHA256 = valid.SHA256[len("sha256:"):]
	tooMany := make([]PublishImageFile, maxPublishImageFiles+1)
	for i := range tooMany {
		tooMany[i] = valid
	}

	for name, files := range map[string][]PublishImageFile{
		"digest":    {wrongDigest},
		"type":      {wrongType},
		"base64":    {badBase64},
		"prefix":    {prefixless},
		"too many":  tooMany,
		"empty":     {},
		"gif-media": {{ContentBase64: valid.ContentBase64, SHA256: valid.SHA256, MediaType: "image/gif"}},
	} {
		t.Run(name, func(t *testing.T) {
			paths, cleanup, err := materializePublishImageFiles(files)
			defer cleanup()
			require.Error(t, err)
			assert.Nil(t, paths)
		})
	}
}

func TestPublishContentUsesVerifiedImageFiles(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service := NewXiaohongshuService()
	var published []string
	service.publishImage = func(_ context.Context, content xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
		for _, path := range content.ImagePaths {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			published = append(published, string(raw))
		}
		return &xiaohongshu.PublishResult{
			FeedID:      "64f1a2b3c4d5e6f7a8b9c0d1",
			EvidenceURL: "https://www.xiaohongshu.com/explore/64f1a2b3c4d5e6f7a8b9c0d1",
		}, nil
	}
	first, second := testPNG(t, 3), testPNG(t, 4)
	req := &PublishRequest{
		RequestID:      "request-1",
		IdempotencyKey: "key-1",
		Title:          "标题",
		Content:        "正文",
		ImageFiles:     []PublishImageFile{imageFile(first, "image/png"), imageFile(second, "image/png")},
	}

	response, err := service.PublishContent(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "VERIFIED", response.Status)
	assert.Equal(t, 2, response.Images)
	assert.Equal(t, []string{string(first), string(second)}, published)
}

func TestPublishContentClassifiesPreSubmitFailures(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	content := testPNG(t, 5)
	called := 0
	service := NewXiaohongshuService()
	service.processImage = func(paths []string) ([]string, error) { return paths, nil }
	service.publishImage = func(context.Context, xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
		called++
		return nil, errors.New("unreachable")
	}
	base := func(key string) *PublishRequest {
		return &PublishRequest{RequestID: "request-" + key, IdempotencyKey: key, Title: "标题", Content: "正文"}
	}

	both := base("both")
	both.Images = []string{"image.png"}
	both.ImageFiles = []PublishImageFile{imageFile(content, "image/png")}
	missing := base("missing")
	longTitle := base("title")
	longTitle.Title = "这是一个远远超过二十个汉字限制的小红书笔记标题用于测试"
	longTitle.ImageFiles = []PublishImageFile{imageFile(content, "image/png")}
	wrongDigest := base("digest")
	wrongDigest.ImageFiles = []PublishImageFile{imageFile(content, "image/png")}
	wrongDigest.ImageFiles[0].SHA256 = "sha256:" + hex.EncodeToString(make([]byte, 32))

	for _, req := range []*PublishRequest{both, missing, longTitle, wrongDigest} {
		_, err := service.PublishContent(context.Background(), req)
		require.Error(t, err)
		assert.ErrorIs(t, err, xiaohongshu.ErrPublishNotSubmitted, req.IdempotencyKey)
	}
	assert.Equal(t, 0, called)
}

func TestPublishContentDisabledSwitchIsNotSubmitted(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "false")
	_, err := NewXiaohongshuService().PublishContent(context.Background(), &PublishRequest{
		RequestID: "request-1", IdempotencyKey: "key-1", Title: "标题", Content: "正文",
		Images: []string{"image.png"},
	})
	assert.ErrorIs(t, err, xiaohongshu.ErrPublishNotSubmitted)
}

func TestPublishHandlerReportsNotSubmittedWithoutDetails(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "true")
	service := NewXiaohongshuService()
	service.processImage = func([]string) ([]string, error) {
		return nil, errors.New("download /private/path failed")
	}
	server := &AppServer{xiaohongshuService: service}
	body := []byte(`{"request_id":"request-1","idempotency_key":"key-1","title":"标题","content":"正文","images":["https://example.invalid/a.png"]}`)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/publish", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	server.publishHandler(c)

	assert.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	var response ErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "PUBLISH_NOT_SUBMITTED", response.Code)
	assert.Nil(t, response.Details)
	assert.NotContains(t, recorder.Body.String(), "/private/path")
}

func TestPublishReadinessReportsSwitchWithoutBrowser(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "false")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/publish/readiness", nil)
	(&AppServer{xiaohongshuService: NewXiaohongshuService()}).publishReadinessHandler(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, "xiaohongshu-mcp.publish-readiness/v1", response.Data["schema"])
	assert.Equal(t, false, response.Data["external_actions_enabled"])
	assert.Equal(t, true, response.Data["image_files_supported"])
}
