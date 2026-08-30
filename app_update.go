package main

// In-app auto-update, exposed to the frontend.
//
// These are thin bindings over internal/updater — the same shape as app_k6.go
// is over internal/perf: construct a per-call service, bound the work with a
// context off a.ctx, and return the package's own result types (Wails
// generates the matching TS models under the `updater` namespace).
//
// The updater package is deliberately UI-framework-free, so the ONE thing it
// can't do itself — quit the running app so the swap helper can replace the
// bundle and relaunch — is done here, where the Wails runtime context lives.

import (
	"context"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/updater"
)

const (
	// checkUpdateTimeout bounds the launch/manual check against the GitHub API.
	checkUpdateTimeout = 20 * time.Second
	// updateDownloadTimeout bounds the whole download+verify+stage cycle. The
	// DMG is ~42MB; generous like app_k6.go's k6DownloadTimeout so a slow link
	// still finishes while the frontend's spinner is guaranteed to end.
	updateDownloadTimeout = 15 * time.Minute
	// installQuitDelay lets the InstallUpdate call return to the frontend (so
	// it can paint "Restarting…") before we quit for the swap helper.
	installQuitDelay = 600 * time.Millisecond
)

func (a *App) updateCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

// CheckForUpdate reports whether a newer release is available. A network
// failure or GitHub rate-limit is NOT an error here — it comes back as
// Available=false with a soft Error string (see updater.Service.Check), so a
// background launch check never raises a banner just because the machine is
// offline.
func (a *App) CheckForUpdate() (updater.Status, error) {
	ctx, cancel := a.updateCtx(checkUpdateTimeout)
	defer cancel()
	return updater.DefaultService().Check(ctx)
}

// DownloadAndVerifyUpdate downloads the latest DMG, verifies it end to end
// (size-capped download, optional published SHA-256, code-signature integrity,
// our Team ID V8SAC4GCQQ, and Apple notarization), and stages the verified
// .app ready to install. It returns the staged paths, or an error describing
// exactly which check failed. Synchronous, like app_k6.go's DownloadK6 — the
// banner shows a "Verifying…" state for the duration.
func (a *App) DownloadAndVerifyUpdate() (updater.StagedUpdate, error) {
	ctx, cancel := a.updateCtx(updateDownloadTimeout)
	defer cancel()
	return updater.DefaultService().DownloadAndStage(ctx)
}

// InstallUpdate swaps in the staged, verified update. On the one-click path it
// launches the detached swap+relaunch helper and then quits AUK (after a short
// delay so this call's result reaches the UI first); the helper waits for the
// quit, replaces the bundle, and reopens it. If the app can't replace itself
// automatically (e.g. it lives somewhere unwritable), it falls back to opening
// the verified DMG for a guided drag-install and returns Guided=true with a
// message — never a silent no-op.
func (a *App) InstallUpdate() (updater.InstallResult, error) {
	ctx, cancel := a.updateCtx(checkUpdateTimeout)
	defer cancel()

	res, err := updater.DefaultService().Install(ctx)
	if err != nil {
		return res, err
	}
	if res.Relaunching {
		// Return first, then quit — the helper is already waiting on our PID.
		go func() {
			time.Sleep(installQuitDelay)
			wailsruntime.Quit(a.ctx)
		}()
	}
	return res, nil
}

// GetUpdatePref reports whether launch-time update checks are enabled
// (default true / opt-out). Stored in the updater's own file under the AUK
// app-support dir, so this needs no change to the shared settings schema.
func (a *App) GetUpdatePref() bool {
	return updater.LoadAutoCheck()
}

// SetUpdatePref persists the auto-check preference.
func (a *App) SetUpdatePref(enabled bool) error {
	return updater.SaveAutoCheck(enabled)
}
