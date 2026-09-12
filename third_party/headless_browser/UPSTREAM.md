# headless_browser local patch

- Upstream: `github.com/xpzouying/headless_browser`
- Baseline: `v0.4.0`
- License: MIT; the original license is preserved in this directory.
- Patch: expose `WithLeakless(bool)` and make the Windows default `false`.

The patch prevents rod from extracting and executing `leakless.exe` on Windows.
The browser still receives the DevTools `Browser.close` command and the launcher
waits for process exit before removing its temporary profile.

To upgrade, diff this directory against the desired upstream tag, carry the
small `Leakless` option/default/launcher change forward, run `go test ./...`, and
repeat the authenticated loopback smoke test documented in
`docs/foundry-huaxiaobao-integration.md`.
