# Sidecars

Third-party binaries bundled with the app as arm's-length CLI sidecars.

## k6 (AGPL-3.0)

`k6` is [Grafana k6](https://github.com/grafana/k6), licensed **AGPL-3.0**. It
is shipped **unmodified** and invoked only as a separate child process
(`os/exec`) — never linked into the app binary, never `go:embed`'d, never
`xk6`-compiled. This arm's-length boundary is what keeps the app's own source
out of the AGPL (see docs/02-architecture.md §11).

Pinned version: **v0.54.0**. It is pinned in three places that must move
together:

| Where | What |
|---|---|
| `download-k6.sh` | `K6_VERSION` + per-target archive/binary SHA-256 (dev sidecar) |
| `internal/perf/download.go` | `K6Version` + the `k6Releases` digest table (in-app self-heal download) |
| `k6-NOTICE.txt` | the version, digests, and Corresponding Source offer that ship to users |

Bumping the version also means re-verifying `internal/perf/runner.go`: the
NDJSON and `handleSummary` parsing was written against v0.54.0's output.

### Files here

- `k6` — the binary itself. **Not committed** (see .gitignore); fetch it with
  `./download-k6.sh macos-arm64`. The script verifies the download against the
  pinned SHA-256 of both the release archive and the extracted binary.
- `k6-LICENSE.txt` — the AGPLv3 text, verbatim from the v0.54.0 tag. Committed.
- `k6-NOTICE.txt` — version, copyright, upstream source, "unmodified" statement,
  pinned digests, and the Corresponding Source offer. Committed.

### What ships to users

`scripts/bundle-k6.sh` (invoked by `scripts/release.sh`) copies all three into
the app bundle:

    AUK.app/Contents/Resources/bin/k6
    AUK.app/Contents/Resources/k6-LICENSE.txt
    AUK.app/Contents/Resources/k6-NOTICE.txt

Distributing the binary is what triggers the AGPL obligations, so the license
text and the source offer must travel with it — that is why `bundle-k6.sh`
hard-fails rather than staging k6 without them.

`internal/perf/download.go` provides the same binary as a fallback download
(pinned URL, pinned digests, installed to
`~/Library/Application Support/AUK/bin/k6`) for a build that shipped without
the bundled copy. It downloads the official upstream release and changes
nothing about it, so the same "unmodified, arm's-length" analysis holds.

Corresponding source for what we ship: the exact upstream release tag at
https://github.com/grafana/k6/releases/tag/v0.54.0
