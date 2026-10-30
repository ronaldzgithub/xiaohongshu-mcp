package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/h2non/filetype"
)

const (
	maxPublishImageFiles     = 9
	maxPublishImageFileBytes = 16 * 1024 * 1024
	maxPublishImageBatch     = 64 * 1024 * 1024
)

var publishImageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var publishImageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// materializePublishImageFiles verifies each submitted image and writes it to a
// private temporary directory. The caller must run cleanup after publishing.
// Every check happens before the browser opens, so a failure is never a
// submitted note.
func materializePublishImageFiles(files []PublishImageFile) ([]string, func(), error) {
	noop := func() {}
	if len(files) == 0 || len(files) > maxPublishImageFiles {
		return nil, noop, fmt.Errorf("image_files 必须包含 1 到 %d 张图片", maxPublishImageFiles)
	}
	decoded := make([][]byte, len(files))
	total := 0
	for i, file := range files {
		extension, ok := publishImageExtensions[file.MediaType]
		if !ok || extension == "" {
			return nil, noop, fmt.Errorf("第%d张图片的 media_type 不受支持", i+1)
		}
		if !publishImageDigestPattern.MatchString(file.SHA256) {
			return nil, noop, fmt.Errorf("第%d张图片的 sha256 格式无效", i+1)
		}
		if file.ContentBase64 == "" || base64.StdEncoding.DecodedLen(len(file.ContentBase64)) > maxPublishImageFileBytes+3 {
			return nil, noop, fmt.Errorf("第%d张图片大小超出限制", i+1)
		}
		content, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
		if err != nil || len(content) == 0 || len(content) > maxPublishImageFileBytes {
			return nil, noop, fmt.Errorf("第%d张图片的 content_base64 无效", i+1)
		}
		total += len(content)
		if total > maxPublishImageBatch {
			return nil, noop, errors.New("图片总大小超出限制")
		}
		sum := sha256.Sum256(content)
		if "sha256:"+hex.EncodeToString(sum[:]) != file.SHA256 {
			return nil, noop, fmt.Errorf("第%d张图片的内容摘要与声明不一致", i+1)
		}
		kind, err := filetype.Match(content)
		if err != nil || kind.MIME.Value != file.MediaType {
			return nil, noop, fmt.Errorf("第%d张图片的实际类型与声明不一致", i+1)
		}
		decoded[i] = content
	}

	directory, err := os.MkdirTemp("", "xhs-publish-")
	if err != nil {
		return nil, noop, fmt.Errorf("创建图片临时目录失败: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return nil, noop, fmt.Errorf("设置图片临时目录权限失败: %w", err)
	}
	paths := make([]string, len(files))
	for i, content := range decoded {
		path := filepath.Join(directory, fmt.Sprintf("image-%02d%s", i+1, publishImageExtensions[files[i].MediaType]))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("写入第%d张图片失败: %w", i+1, err)
		}
		written, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(written) != sha256.Sum256(content) {
			cleanup()
			return nil, noop, fmt.Errorf("第%d张图片写入后校验失败", i+1)
		}
		paths[i] = path
	}
	return paths, cleanup, nil
}
