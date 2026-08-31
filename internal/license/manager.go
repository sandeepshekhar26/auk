// Package license implements AUK's offline-first licensing and trial.
//
// The whole design serves one product promise: "not a subscription, no cloud,
// your data never leaves your Mac." Concretely that means:
//
//   - A 14-day trial runs with no account and no network (trial.go).
//   - Activation is the ONE moment a network call happens (activator.go). It
//     exchanges a Merchant-of-Record key for a SignedLicense — a license
//     document signed by AUK's Ed25519 key.
//   - Forever after, the app verifies that SignedLicense OFFLINE (keys.go,
//     status.go). A valid, machine-bound license is "licensed" with no server
//     contact, ever. Our server going down, or the user being offline, cannot
//     brick a paying customer.
//
// Trust flows from ONE thing: a signature, over a canonical byte encoding
// (model.go), made by a private key AUK never ships. Everything else —
// keychain storage, the file mirror, the trial file — is convenience or
// resilience, never a trust root.
//
// Manager is the package's single entry point; app_license.go (package main)
// is a thin Wails binding over it.
package license

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager owns licensing state and is safe for concurrent use — every public
// method takes mu, so a status poll from the UI can't race an activation.
type Manager struct {
	mu sync.Mutex

	dataDir   string
	kc        Keychain
	fp        *Fingerprinter
	activator Activator
	// verifyKey is the public key licenses are checked against. Production
	// wires the embedded key; tests inject an ephemeral one so the same
	// verification path runs without needing the embedded key's private half.
	verifyKey ed25519.PublicKey
	now       func() time.Time

	ls    *licenseStore
	trial *trialTracker
}

// New builds the production Manager: OS keychain, macOS hardware fingerprint,
// the (currently stubbed) remote activator, and the embedded verification
// key. Returns an error only if the compiled-in public key is unpar-seable,
// which would be a build defect.
func New() (*Manager, error) {
	pub, err := embeddedPublicKey()
	if err != nil {
		return nil, err
	}
	kc := NewKeyringKeychain()
	return newManagerWith(defaultDataDir(), kc, NewFingerprinter(kc), remoteActivator{}, pub, time.Now), nil
}

// newManagerWith is the shared constructor (production + tests) that wires the
// license store and trial tracker onto a set of collaborators.
func newManagerWith(dataDir string, kc Keychain, fp *Fingerprinter, act Activator, verifyKey ed25519.PublicKey, now func() time.Time) *Manager {
	return &Manager{
		dataDir:   dataDir,
		kc:        kc,
		fp:        fp,
		activator: act,
		verifyKey: verifyKey,
		now:       now,
		ls:        &licenseStore{kc: kc, dataDir: dataDir},
		trial:     &trialTracker{kc: kc, dataDir: dataDir, now: now},
	}
}

// defaultDataDir is ~/.auk — the same app-support dir settings.yaml,
// history.jsonl and the MCP token already live in.
func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".auk")
	}
	return ".auk"
}

// Status resolves the single source of truth for licensing state. It is cheap
// enough to call on demand (a keychain read + a fingerprint read) and has no
// side effects beyond the trial tracker's self-healing persistence.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() Status {
	now := m.now().UTC()
	machineID, _ := m.fp.MachineID()

	if stored, ok := m.ls.load(); ok {
		return m.resolveLicensedLocked(stored, machineID, now)
	}

	ts := m.trial.resolve()
	if ts.Expired {
		return Status{State: StateTrialExpired, DaysRemaining: 0, MachineID: machineID}
	}
	return Status{State: StateTrial, DaysRemaining: ts.DaysRemaining, MachineID: machineID}
}

// resolveLicensedLocked turns a stored license into a Status. The ordering is
// deliberate and is the security core of the read path: signature first, then
// machine binding, and ONLY those two can produce StateLicenseInvalid. Past
// that point the license is licensed and stays licensed — updates-window and
// grace are advisory flags layered on top, never downgrades (see status.go).
func (m *Manager) resolveLicensedLocked(stored storedLicense, machineID string, now time.Time) Status {
	sl := stored.Signed
	lic := sl.License
	st := Status{
		Email:        lic.Email,
		Name:         lic.Name,
		Plan:         lic.Plan,
		MachineCount: lic.MachineCount,
		MaxMachines:  lic.MaxMachines,
		MachineID:    machineID,
	}

	if err := verifyWith(m.verifyKey, sl); err != nil {
		st.State = StateLicenseInvalid
		st.Message = "This license failed its signature check."
		return st
	}
	if lic.MachineID != machineID {
		st.State = StateLicenseInvalid
		st.Message = "This license is activated on a different machine. Re-activate to use AUK here."
		return st
	}

	st.State = StateLicensed
	if !lic.ExpiresUpdatesAt.IsZero() {
		st.UpdatesExpireAt = lic.ExpiresUpdatesAt.UTC().Format(time.RFC3339)
		if now.After(lic.ExpiresUpdatesAt) {
			st.UpdatesExpired = true
			st.Message = "Your 12 months of updates have ended. AUK keeps working — only updates released after this date need a renewal."
		}
	}
	inGrace, graceDays := graceState(stored.LastValidatedAt, now, time.Duration(GraceDays)*24*time.Hour)
	st.InGrace = inGrace
	st.GraceDaysRemaining = graceDays
	return st
}

// Activate turns a pasted license key OR a signed license file into a stored,
// verified, machine-bound license and returns the resulting Status. It funnels
// both inputs through one verify-and-store path:
//
//   - If the input is itself a signed license (JSON, or base64-of-JSON, as
//     cmd/mklicense emits), it is used directly — OFFLINE activation. This is
//     what makes the dev/test loop work today, and doubles as a real feature
//     for air-gapped customers.
//   - Otherwise it is treated as an MoR key and handed to the Activator (the
//     production remote path, currently a stub that asks the user to paste a
//     license file instead).
func (m *Manager) Activate(ctx context.Context, keyOrLicense string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	input := strings.TrimSpace(keyOrLicense)
	if input == "" {
		return m.statusLocked(), errors.New("enter a license key or paste a license file")
	}

	machineID, err := m.fp.MachineID()
	if err != nil {
		return m.statusLocked(), fmt.Errorf("could not identify this machine: %w", err)
	}

	if sl, ok := tryParseSignedLicense(input); ok {
		return m.storeVerifiedLocked(sl, machineID, now)
	}

	sl, err := m.activator.Activate(ctx, input, machineID)
	if err != nil {
		return m.statusLocked(), err
	}
	return m.storeVerifiedLocked(sl, machineID, now)
}

// storeVerifiedLocked verifies a SignedLicense against the same two gates the
// read path uses (signature, machine binding) BEFORE persisting it, so a bad
// or wrong-machine license is rejected at activation with a clear message
// rather than being stored and then read back as StateLicenseInvalid.
func (m *Manager) storeVerifiedLocked(sl SignedLicense, machineID string, now time.Time) (Status, error) {
	if err := verifyWith(m.verifyKey, sl); err != nil {
		return m.statusLocked(), errors.New("that license didn't pass its signature check — it may be corrupted or not a genuine AUK license")
	}
	if sl.License.MachineID != machineID {
		return m.statusLocked(), fmt.Errorf(
			"that license was issued for a different machine (%s), not this one (%s) — activate with a license for this machine",
			short(sl.License.MachineID), short(machineID))
	}
	stored := storedLicense{Signed: sl, ActivatedAt: now, LastValidatedAt: now}
	if err := m.ls.save(stored); err != nil {
		return m.statusLocked(), fmt.Errorf("couldn't save the license: %w", err)
	}
	return m.statusLocked(), nil
}

// Deactivate removes the license from this machine and returns to the trial
// (which may itself be expired). Deactivating with nothing stored is a no-op.
//
// It also asks the signing worker to release this machine's SEAT, so the
// licence can be activated on another Mac — but that call is best-effort and
// its failure is deliberately swallowed. Local removal must always succeed: a
// user wiping a machine, or deactivating on a plane, cannot be told "no, you
// are still offline". A seat left stale server-side is a support ticket; a
// deactivation that refuses to run is a customer who cannot move their licence
// at all. The stale-seat case is exactly what the deactivate endpoint's
// idempotence covers — re-running it later clears the seat.
func (m *Manager) Deactivate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if stored, ok := m.ls.load(); ok {
		if releaser, canRelease := m.activator.(seatReleaser); canRelease {
			ctx, cancel := context.WithTimeout(context.Background(), activationTimeout)
			_ = releaser.Deactivate(ctx, stored.Signed.License.LicenseKey, stored.Signed.License.MachineID)
			cancel()
		}
	}
	return m.ls.clear()
}

// StartTrialIfNeeded records the trial start on first ever call and is a
// harmless no-op afterwards (and for licensed users). Meant to be called once
// per launch. It never resets or extends an existing trial.
func (m *Manager) StartTrialIfNeeded() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.ls.load(); ok {
		return // licensed — no trial clock needed
	}
	_ = m.trial.resolve()
}

// MachineID exposes this machine's fingerprint (for support/debugging and the
// "this machine" label in the UI).
func (m *Manager) MachineID() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fp.MachineID()
}

// tryParseSignedLicense recognizes a signed license file supplied where a key
// was expected. It accepts raw JSON or base64-of-JSON. A bare MoR key (not
// JSON, not base64-of-JSON) fails both and falls through to the key path. The
// Signature+identity guards stop an unrelated JSON blob from being mistaken
// for a license.
func tryParseSignedLicense(s string) (SignedLicense, bool) {
	looksLikeLicense := func(sl SignedLicense) bool {
		return sl.Signature != "" && (sl.License.LicenseKey != "" || sl.License.Email != "")
	}
	var sl SignedLicense
	if json.Unmarshal([]byte(s), &sl) == nil && looksLikeLicense(sl) {
		return sl, true
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		var sl2 SignedLicense
		if json.Unmarshal(raw, &sl2) == nil && looksLikeLicense(sl2) {
			return sl2, true
		}
	}
	return SignedLicense{}, false
}

// short trims a fingerprint for user-facing error messages.
func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}
