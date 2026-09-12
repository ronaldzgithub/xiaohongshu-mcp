package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBrowserUsesExactPinnedBinary(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "chrome.exe")
	content := []byte("sandbox-browser-fixture")
	if err := os.WriteFile(binPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	t.Setenv(browserBinaryEnv, binPath)
	t.Setenv(browserBinarySHA256Env, hex.EncodeToString(digest[:]))

	got, err := EnsureBrowser()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(binPath) {
		t.Fatalf("unexpected pinned browser path: %s", got)
	}
}

func TestEnsureBrowserRejectsPinnedBinaryDrift(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "chrome.exe")
	if err := os.WriteFile(binPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(browserBinaryEnv, binPath)
	t.Setenv(browserBinarySHA256Env, strings.Repeat("0", 64))

	_, err := EnsureBrowser()
	if err == nil || !strings.Contains(err.Error(), "SHA256 不匹配") {
		t.Fatalf("expected pinned hash mismatch, got %v", err)
	}
}

func TestEnsureBrowserRejectsIncompletePinnedBinding(t *testing.T) {
	t.Setenv(browserBinaryEnv, "")
	t.Setenv(browserBinarySHA256Env, strings.Repeat("0", 64))

	_, err := EnsureBrowser()
	if err == nil || !strings.Contains(err.Error(), browserBinaryEnv) {
		t.Fatalf("expected missing pinned path error, got %v", err)
	}
}
