package updater

// Service ties the pieces together: it resolves the current version, asks a
// Feed for the latest release, decides whether an update is warranted, and —
// on request — downloads, verifies, stages, and installs it. It holds only
// configuration; all cross-call state (a staged, verified update awaiting
// install) lives on disk in the pending-update dir, so the Wails bindings can
// construct a fresh Service per call (like app_k6.go's perf.NewRunner) without
// needing any new field on *App.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// AUK's release coordinates. AssetPrefix/Suffix bracket the DMG asset name so
// "AUK-0.3.0.dmg" is selected over a sibling .zip or checksums file.
const (
	DefaultRepo        = "sandeepshekhar26/auk"
	DefaultAssetPrefix = "AUK-"
	DefaultAssetSuffix = ".dmg"
)

// Service is the updater's public entry point.
type Service struct {
	Feed       Feed
	TeamID     string
	HTTPClient *http.Client
	// currentVersion is resolved from the bundle by default; overridable for
	// tests.
	currentVersion string
	// runner executes hdiutil/codesign/spctl/ditto; execRunner by default,
	// stubbed in tests.
	runner runner
}

// DefaultService returns the AUK-configured updater backed by the public
// GitHub releases feed.
func DefaultService() *Service {
	return &Service{
		Feed: GitHubFeed{
			Repo:        DefaultRepo,
			AssetPrefix: DefaultAssetPrefix,
			AssetSuffix: DefaultAssetSuffix,
		},
		TeamID: DefaultTeamID,
		runner: execRunner{},
	}
}

func (s *Service) teamID() string {
	if s.TeamID != "" {
		return s.TeamID
	}
	return DefaultTeamID
}

func (s *Service) cmd() runner {
	if s.runner != nil {
		return s.runner
	}
	return execRunner{}
}

func (s *Service) current() string {
	if s.currentVersion != "" {
		return s.currentVersion
	}
	return CurrentVersion()
}

// Status is the result of a check — the CheckForUpdate binding's return shape.
type Status struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	URL            string `json:"url"`
	Notes          string `json:"notes"`
	SizeBytes      int64  `json:"sizeBytes"`
	// IsDevBuild is true for an unversioned/dev build; the UI never nags then.
	IsDevBuild bool `json:"isDevBuild"`
	// Error carries a soft reason the check couldn't complete (offline,
	// rate-limited). It is NOT surfaced as a loud failure — Available is simply
	// false — but lets the UI explain "couldn't check" if it wants to.
	Error string `json:"error,omitempty"`
}

// Check resolves whether a newer release is available. A network failure or
// GitHub rate-limit is folded into Status (Available=false, Error set) and
// returns a nil error: a background launch check must never crash or raise a
// red banner just because the machine is offline. Only genuinely unexpected
// conditions would return a non-nil error (there are none today).
func (s *Service) Check(ctx context.Context) (Status, error) {
	cur := s.current()
	st := Status{
		CurrentVersion: cur,
		IsDevBuild:     IsDevVersion(cur),
	}

	rel, err := s.Feed.Latest(ctx)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}

	st.LatestVersion = rel.Version
	st.URL = rel.URL
	st.Notes = rel.Notes
	st.SizeBytes = rel.SizeBytes

	// Dev/unknown builds are never told an update is available (IsNewer already
	// enforces this, but keep the flag honest for the UI).
	if st.IsDevBuild {
		st.Available = false
		return st, nil
	}
	st.Available = IsNewer(rel.Version, cur)
	return st, nil
}

// DownloadAndStage performs the full verified-download pipeline and leaves a
// staged, verified .app (plus the verified DMG) in the pending-update dir,
// ready for Install. Every failure returns an error and installs nothing. The
// caller owns the timeout via ctx (this is a ~42 MB download).
func (s *Service) DownloadAndStage(ctx context.Context) (StagedUpdate, error) {
	rel, err := s.Feed.Latest(ctx)
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("look up latest release: %w", err)
	}
	if rel.URL == "" {
		return StagedUpdate{}, fmt.Errorf("latest release has no downloadable asset")
	}

	stageDir, err := pendingDir()
	if err != nil {
		return StagedUpdate{}, err
	}
	// Fresh staging area every attempt — never install bits left over from a
	// previous, possibly-interrupted run.
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return StagedUpdate{}, fmt.Errorf("create staging dir: %w", err)
	}

	assetName := rel.AssetName
	if assetName == "" {
		assetName = "AUK-update.dmg"
	}
	dmgPath := filepath.Join(stageDir, assetName)

	// 1. Bounded download.
	if err := downloadDMG(ctx, s.HTTPClient, rel.URL, dmgPath); err != nil {
		return StagedUpdate{}, err
	}

	// 2. Checksum, if the release published one.
	if rel.SHA256 != "" {
		if err := verifySHA256(dmgPath, rel.SHA256); err != nil {
			return StagedUpdate{}, fmt.Errorf("DMG failed checksum: %w", err)
		}
	}

	// Mount, verify the .app, stage it. Always detach.
	mount, err := attachDMG(ctx, s.cmd(), dmgPath)
	if err != nil {
		return StagedUpdate{}, err
	}
	defer func() { _ = detachDMG(ctx, s.cmd(), mount) }()

	srcApp, err := findAppBundle(mount)
	if err != nil {
		return StagedUpdate{}, err
	}

	// 3–5. Signature integrity + our Team ID + notarization, on the mounted
	// bundle, before we spend time copying it.
	if err := verifyAppBundle(ctx, s.cmd(), srcApp, s.teamID()); err != nil {
		return StagedUpdate{}, err
	}

	stagedApp := filepath.Join(stageDir, filepath.Base(srcApp))
	if err := stageApp(ctx, s.cmd(), srcApp, stagedApp); err != nil {
		return StagedUpdate{}, err
	}

	// Re-verify the STAGED copy — the exact bits Install will swap in. ditto
	// preserves the signature, so this must still pass; if it somehow doesn't,
	// reject rather than install an unverifiable copy.
	if err := verifyAppBundle(ctx, s.cmd(), stagedApp, s.teamID()); err != nil {
		return StagedUpdate{}, fmt.Errorf("staged copy failed verification: %w", err)
	}

	return StagedUpdate{
		Version:   rel.Version,
		AppPath:   stagedApp,
		DMGPath:   dmgPath,
		StagedDir: stageDir,
	}, nil
}

// InstallResult tells the binding what happened so it can decide whether to
// quit-and-relaunch or just show guidance.
type InstallResult struct {
	// Relaunching is true when the one-click swap was launched; the caller
	// should quit the app now so the helper can replace the bundle and reopen.
	Relaunching bool `json:"relaunching"`
	// Guided is true when the swap couldn't be done automatically (e.g. the
	// app lives somewhere unwritable) and the DMG was opened for a manual
	// drag-install instead.
	Guided bool `json:"guided"`
	// Message is a human-readable explanation for the guided path.
	Message string `json:"message,omitempty"`
	// Version is the version being installed.
	Version string `json:"version"`
}

// FindStaged locates a previously staged+verified update, or an error if none
// is ready (DownloadAndStage hasn't run, or its dir was cleared).
func (s *Service) FindStaged() (StagedUpdate, error) {
	stageDir, err := pendingDir()
	if err != nil {
		return StagedUpdate{}, err
	}
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("no staged update found — download it first")
	}
	su := StagedUpdate{StagedDir: stageDir}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && filepath.Ext(name) == ".app" {
			su.AppPath = filepath.Join(stageDir, name)
		}
		if filepath.Ext(name) == ".dmg" {
			su.DMGPath = filepath.Join(stageDir, name)
		}
	}
	if su.AppPath == "" {
		return StagedUpdate{}, fmt.Errorf("no staged app found — download it first")
	}
	return su, nil
}

// Install swaps in a staged, verified update. It re-verifies the staged bundle
// one more time (closing any gap between stage and install), then either:
//   - spawns the detached swap+relaunch helper and reports Relaunching=true
//     (the caller must then quit the app), or
//   - if the destination bundle is not writable, opens the verified DMG for a
//     guided drag-install and reports Guided=true.
//
// The runtime Quit is intentionally left to the caller (app_update.go), which
// has the Wails context; the updater package stays UI-framework-free.
func (s *Service) Install(ctx context.Context) (InstallResult, error) {
	su, err := s.FindStaged()
	if err != nil {
		return InstallResult{}, err
	}

	// Re-verify right before install — the staged bundle sat in a user-writable
	// dir since download; don't trust it blindly now.
	if err := verifyAppBundle(ctx, s.cmd(), su.AppPath, s.teamID()); err != nil {
		return InstallResult{}, fmt.Errorf("staged update no longer verifies, refusing to install: %w", err)
	}

	dest, err := currentAppBundle()
	if err != nil {
		// Dev build / not a bundle: nothing to swap. Offer the DMG instead.
		return s.guide(su, fmt.Sprintf("This build isn't an installed .app (%v).", err))
	}

	if !dirWritable(filepath.Dir(dest)) {
		return s.guide(su, fmt.Sprintf("%s isn't writable, so AUK can't replace itself there automatically.", filepath.Dir(dest)))
	}

	if err := spawnSwapAndRelaunch(su.StagedDir, su.AppPath, dest); err != nil {
		// If the helper couldn't even start, fall back to the guided path
		// rather than leaving the user stuck.
		return s.guide(su, fmt.Sprintf("Couldn't start the installer helper (%v).", err))
	}
	return InstallResult{Relaunching: true, Version: su.Version}, nil
}

// guide opens the verified DMG in Finder and returns instructions for a manual
// drag-install.
func (s *Service) guide(su StagedUpdate, why string) (InstallResult, error) {
	msg := why + " The verified update has been opened in Finder — drag AUK to Applications to finish."
	if su.DMGPath != "" {
		_, _, _ = s.cmd().run(context.Background(), "open", su.DMGPath)
	} else if su.AppPath != "" {
		_, _, _ = s.cmd().run(context.Background(), "open", "-R", su.AppPath)
	}
	return InstallResult{Guided: true, Message: msg, Version: su.Version}, nil
}
