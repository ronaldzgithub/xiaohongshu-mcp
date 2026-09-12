package headless_browser

import (
	"runtime"
	"testing"
)

func TestLeaklessDefaultByOperatingSystem(t *testing.T) {
	if leaklessDefault("windows") {
		t.Fatal("Windows must not enable the leakless helper")
	}
	if !leaklessDefault("linux") || !leaklessDefault("darwin") {
		t.Fatal("non-Windows defaults must preserve upstream leakless behavior")
	}
}

func TestLeaklessOptionOverridesDefault(t *testing.T) {
	cfg := newDefaultConfig()
	if cfg.Leakless != (runtime.GOOS != "windows") {
		t.Fatalf("unexpected platform default: %v", cfg.Leakless)
	}

	WithLeakless(false)(cfg)
	if cfg.Leakless {
		t.Fatal("WithLeakless(false) did not disable leakless")
	}
	WithLeakless(true)(cfg)
	if !cfg.Leakless {
		t.Fatal("WithLeakless(true) did not enable leakless")
	}
}
