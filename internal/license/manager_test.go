package license

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestManagerStartsInTrial(t *testing.T) {
	env := newTestEnv(t)
	st := env.m.Status()
	if st.State != StateTrial {
		t.Fatalf("State = %q, want %q", st.State, StateTrial)
	}
	if st.DaysRemaining != TrialDays {
		t.Errorf("DaysRemaining = %d, want %d", st.DaysRemaining, TrialDays)
	}
	if st.MachineID != env.machineID {
		t.Errorf("MachineID = %q, want %q", st.MachineID, env.machineID)
	}
}

func TestActivateViaMockKeyThenLicensed(t *testing.T) {
	env := newTestEnv(t)
	st, err := env.m.Activate(context.Background(), "AUK-REAL-LOOKING-KEY")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if st.State != StateLicensed {
		t.Fatalf("State = %q, want %q", st.State, StateLicensed)
	}
	if st.Email != "buyer@example.com" {
		t.Errorf("Email = %q, want buyer@example.com", st.Email)
	}
	if st.MaxMachines != DefaultMaxMachines || st.MachineCount != 1 {
		t.Errorf("seats = %d/%d, want 1/%d", st.MachineCount, st.MaxMachines, DefaultMaxMachines)
	}
	if !st.InGrace {
		t.Error("freshly activated license should be within grace")
	}
}

func TestActivateViaOfflineLicenseFile(t *testing.T) {
	env := newTestEnv(t)
	sl := env.signFor(env.machineID, nil)
	blob, _ := json.Marshal(sl)

	st, err := env.m.Activate(context.Background(), string(blob))
	if err != nil {
		t.Fatalf("offline activation failed: %v", err)
	}
	if st.State != StateLicensed {
		t.Fatalf("State = %q, want licensed", st.State)
	}
}

func TestActivateRejectsTamperedLicenseFile(t *testing.T) {
	env := newTestEnv(t)
	sl := env.signFor(env.machineID, nil)

	// Tamper a signed field, keeping the (now invalid) signature.
	sl.License.Email = "attacker@evil.com"
	blob, _ := json.Marshal(sl)

	st, err := env.m.Activate(context.Background(), string(blob))
	if err == nil {
		t.Fatal("tampered license was accepted")
	}
	if st.State == StateLicensed {
		t.Fatal("tampered license produced a licensed state")
	}
	// Nothing should have been stored — we're still in trial.
	if got := env.m.Status().State; got != StateTrial {
		t.Errorf("after rejected activation State = %q, want trial", got)
	}
}

func TestActivateRejectsWrongMachine(t *testing.T) {
	env := newTestEnv(t)
	// Validly signed, but bound to a different machine.
	sl := env.signFor("hw-someothermachineaaaaaaaaaaaaaa", nil)
	blob, _ := json.Marshal(sl)

	_, err := env.m.Activate(context.Background(), string(blob))
	if err == nil {
		t.Fatal("license bound to another machine was accepted")
	}
	if got := env.m.Status().State; got != StateTrial {
		t.Errorf("after rejected activation State = %q, want trial", got)
	}
}

func TestActivateEmptyInput(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "   "); err == nil {
		t.Error("empty activation input should error")
	}
}

// TestStoredTamperedResolvesInvalid proves the READ path is a real gate: even
// if a tampered blob is planted directly in the keychain (bypassing Activate),
// Status must report license_invalid, not licensed.
func TestStoredTamperedResolvesInvalid(t *testing.T) {
	env := newTestEnv(t)
	sl := env.signFor(env.machineID, nil)
	sl.License.Plan = "enterprise" // invalidates signature
	stored := storedLicense{Signed: sl, ActivatedAt: env.clock.now(), LastValidatedAt: env.clock.now()}
	b, _ := json.Marshal(stored)
	_ = env.kc.Set(keychainService, accountLicense, string(b))

	st := env.m.Status()
	if st.State != StateLicenseInvalid {
		t.Fatalf("State = %q, want %q", st.State, StateLicenseInvalid)
	}
}

// TestStoredWrongMachineResolvesInvalid: a perfectly-signed license that
// belongs to another machine reads as invalid on this one.
func TestStoredWrongMachineResolvesInvalid(t *testing.T) {
	env := newTestEnv(t)
	sl := env.signFor("hw-anothermachinebbbbbbbbbbbbbbbb", nil)
	stored := storedLicense{Signed: sl, ActivatedAt: env.clock.now(), LastValidatedAt: env.clock.now()}
	b, _ := json.Marshal(stored)
	_ = env.kc.Set(keychainService, accountLicense, string(b))

	if st := env.m.Status(); st.State != StateLicenseInvalid {
		t.Fatalf("State = %q, want %q", st.State, StateLicenseInvalid)
	}
}

// TestExpiredUpdatesStillLicensed: past the updates window the license keeps
// RUNNING (State licensed) but is flagged. This is the perpetual-license
// promise.
func TestExpiredUpdatesStillLicensed(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// Jump past the 12-month updates window.
	env.clock.advance(400 * 24 * time.Hour)

	st := env.m.Status()
	if st.State != StateLicensed {
		t.Fatalf("State = %q, want still licensed past updates window", st.State)
	}
	if !st.UpdatesExpired {
		t.Error("UpdatesExpired should be true past the window")
	}
	if st.UpdatesExpireAt == "" {
		t.Error("UpdatesExpireAt should be populated for display")
	}
}

// TestGraceLapsesButStaysLicensed: long after the last online check, a valid
// license is STILL licensed (never bricked) — grace lapsing is advisory only.
func TestGraceLapsesButStaysLicensed(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	env.clock.advance(time.Duration(GraceDays+5) * 24 * time.Hour)

	st := env.m.Status()
	if st.State != StateLicensed {
		t.Fatalf("State = %q, want licensed after grace lapse (never brick a paid license)", st.State)
	}
	if st.InGrace {
		t.Error("InGrace should be false after the grace window")
	}
}

func TestDeactivateReturnsToTrial(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := env.m.Deactivate(); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if st := env.m.Status(); st.State != StateTrial {
		t.Errorf("after deactivate State = %q, want trial", st.State)
	}
	// Deactivating again is a no-op, not an error.
	if err := env.m.Deactivate(); err != nil {
		t.Errorf("second Deactivate errored: %v", err)
	}
}

func TestTrialExpiresWithoutLicense(t *testing.T) {
	env := newTestEnv(t)
	env.m.StartTrialIfNeeded()
	env.clock.advance((TrialDays + 1) * 24 * time.Hour)
	if st := env.m.Status(); st.State != StateTrialExpired {
		t.Errorf("State = %q, want %q", st.State, StateTrialExpired)
	}
}

// TestLicensePersistsAcrossManagerInstances: activate, then a brand-new
// Manager over the same stores must resolve licensed purely OFFLINE (the whole
// point — no re-activation, no server).
func TestLicensePersistsAcrossManagerInstances(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	fp2 := &Fingerprinter{kc: env.kc, hostID: func() (string, bool) { return hostA, true }}
	m2 := newManagerWith(env.dir, env.kc, fp2, mockActivator{}, env.pub, env.clock.now)
	if st := m2.Status(); st.State != StateLicensed {
		t.Fatalf("fresh Manager did not read stored license: State = %q", st.State)
	}
}

// TestLicenseSurvivesKeychainLossViaFileMirror: wipe the keychain entry after
// activation; the file mirror must restore licensed state (resilience against
// a keychain reset bricking a paid user).
func TestLicenseSurvivesKeychainLossViaFileMirror(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// Simulate a keychain reset: drop the license entry (file mirror remains).
	_ = env.kc.Delete(keychainService, accountLicense)

	if st := env.m.Status(); st.State != StateLicensed {
		t.Fatalf("license did not survive keychain loss: State = %q", st.State)
	}
	// And it should have self-healed back into the keychain.
	if _, err := env.kc.Get(keychainService, accountLicense); err != nil {
		t.Error("keychain entry was not restored from the file mirror")
	}
}

func TestStartTrialIfNeededSkipsLicensed(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.Activate(context.Background(), "AUK-KEY"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// Should not write any trial record for a licensed user.
	env.m.StartTrialIfNeeded()
	if _, err := env.kc.Get(keychainService, accountTrial); err == nil {
		t.Error("StartTrialIfNeeded started a trial clock for a licensed user")
	}
}
