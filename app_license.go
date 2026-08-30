package main

import (
	"context"
	"sync"

	"apitool/internal/license"
)

// ---- Licensing + trial bindings ----
//
// These are the Wails-bound entry points for AUK's offline-first licensing
// (internal/license). They are deliberately thin: every method is a pass-
// through to a single license.Manager, which is the source of truth for
// trial/licensed/expired state and does all the crypto, keychain, and trial
// bookkeeping. See docs/06-licensing.md.
//
// The Manager is held in a package-level singleton rather than as a field on
// *App, because *App is defined in app.go (owned by the integrator) and this
// file must not edit it. There is exactly one App per process, so a lazily-
// initialized package var is equivalent to an App field here, and it keeps the
// whole feature contained to this file plus the internal/license package.

var (
	licenseMgrOnce sync.Once
	licenseMgr     *license.Manager
	licenseMgrErr  error
)

// licenseManager lazily constructs the process-wide license.Manager. The only
// way license.New can fail is a malformed compiled-in public key (a build
// defect), so the error is cached and surfaced to the UI as an invalid state
// rather than crashing the app.
func licenseManager() (*license.Manager, error) {
	licenseMgrOnce.Do(func() {
		licenseMgr, licenseMgrErr = license.New()
	})
	return licenseMgr, licenseMgrErr
}

// licenseCtx returns a usable context even if startup() hasn't populated a.ctx
// yet. Activation is the only method that takes a context, and only its future
// remote-activator implementation will actually use it.
func (a *App) licenseCtx() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// subsystemError renders a licensing-subsystem startup failure as a Status the
// UI can display without special-casing. It should never occur in a correct
// build (see licenseManager).
func subsystemError(err error) license.Status {
	return license.Status{
		State:   license.StateLicenseInvalid,
		Message: "Licensing failed to start: " + err.Error(),
	}
}

// LicenseStatus returns the single source of truth for the app's licensing
// state: trial / licensed / trial_expired / license_invalid, plus days
// remaining, identity, seat count, and the advisory updates/grace flags. Safe
// to call as often as the UI likes — it's a keychain read plus a fingerprint
// read, with no network.
func (a *App) LicenseStatus() license.Status {
	m, err := licenseManager()
	if err != nil {
		return subsystemError(err)
	}
	return m.Status()
}

// ActivateLicense activates a pasted license key OR a signed license file
// (offline activation — the two are auto-detected). It returns the RESULTING
// status so the frontend can update in one round trip, plus an error with a
// user-facing message when activation is rejected (bad signature, wrong
// machine, or — until the Merchant-of-Record path is wired — an online key,
// which currently asks the user to paste a license file instead).
func (a *App) ActivateLicense(key string) (license.Status, error) {
	m, err := licenseManager()
	if err != nil {
		return subsystemError(err), err
	}
	return m.Activate(a.licenseCtx(), key)
}

// DeactivateLicense removes the license from THIS machine and returns the
// resulting status (which falls back to the trial, possibly expired). It clears
// local state only; releasing the seat with the MoR so it can be used on
// another machine is the server-side call documented in docs/06-licensing.md.
func (a *App) DeactivateLicense() (license.Status, error) {
	m, err := licenseManager()
	if err != nil {
		return subsystemError(err), err
	}
	if derr := m.Deactivate(); derr != nil {
		return m.Status(), derr
	}
	return m.Status(), nil
}

// StartTrialIfNeeded records the trial-start timestamp on first ever launch
// and is a harmless no-op on every launch after (and for licensed users). It
// returns the current status so the frontend can seed its state with a single
// onMount call. Idempotent — it never resets or extends an existing trial.
func (a *App) StartTrialIfNeeded() license.Status {
	m, err := licenseManager()
	if err != nil {
		return subsystemError(err)
	}
	m.StartTrialIfNeeded()
	return m.Status()
}
