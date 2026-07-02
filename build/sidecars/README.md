# Sidecars

Third-party binaries bundled with the app as arm's-length CLI sidecars.

## k6 (AGPL-3.0)

`k6` is [Grafana k6](https://github.com/grafana/k6), licensed **AGPL-3.0**. It
is shipped **unmodified** and invoked only as a separate child process
(`os/exec`) — never linked into the app binary, never `go:embed`'d, never
`xk6`-compiled. This arm's-length boundary is what keeps the app's own source
out of the AGPL (see docs/02-architecture.md §11).

The binary is **not committed** (see .gitignore). Fetch it with:

    ./download-k6.sh macos-arm64

Pinned version: **v0.54.0**. Corresponding source: the exact upstream release
tag at https://github.com/grafana/k6/releases/tag/v0.54.0 (a distributed build
must ship this offer + the AGPL license text alongside the binary).
