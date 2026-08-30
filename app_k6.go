package main

// k6 sidecar self-heal, exposed to the frontend.
//
// A released AUK ships k6 inside the app bundle at Contents/Resources/bin/k6
// (staged and individually code-signed by scripts/release.sh). This binding is
// the fallback for the cases where that copy is not there: a `go run`/`wails
// dev` checkout without build/sidecars/k6, a build produced without the
// packaging step, or a bundle whose k6 a user removed. The load-test panel
// calls it when CheckK6 reports k6 missing.

import (
	"context"
	"fmt"
	"time"

	"apitool/internal/perf"
)

// k6DownloadTimeout bounds the whole fetch-verify-install cycle. The archive
// is ~29MB, so this is generous enough for a slow connection while still
// guaranteeing the frontend's spinner ends.
const k6DownloadTimeout = 15 * time.Minute

// DownloadK6 downloads the pinned official k6 release for this OS/arch,
// verifies it against pinned SHA-256 digests (archive and extracted binary),
// installs it at ~/Library/Application Support/AUK/bin/k6, and returns that
// path. ResolveK6 picks it up from there on the next run.
//
// It is synchronous: the panel shows a spinner for the duration rather than
// this having to grow a progress-event channel for a one-off ~29MB fetch.
func (a *App) DownloadK6() (string, error) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, k6DownloadTimeout)
	defer cancel()

	path, err := perf.DownloadK6(ctx)
	if err != nil {
		return "", fmt.Errorf("could not download k6 %s: %w", perf.K6Version, err)
	}
	return path, nil
}

// K6Version reports the k6 release AUK bundles and downloads, so the UI and
// any third-party-licenses screen can name it without hardcoding a copy.
func (a *App) K6Version() string {
	return perf.K6Version
}
