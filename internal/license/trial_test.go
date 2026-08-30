package license

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTrialTracker(t *testing.T) (*trialTracker, *fakeKeychain, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	kc := newFakeKeychain()
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	return &trialTracker{kc: kc, dataDir: dir, now: clock.now}, kc, clock, dir
}

func TestTrialFirstRunGivesFullTerm(t *testing.T) {
	tr, _, _, _ := newTrialTracker(t)
	st := tr.resolve()
	if st.Expired {
		t.Fatal("brand-new trial reported expired")
	}
	if st.DaysRemaining != TrialDays {
		t.Errorf("DaysRemaining = %d, want %d on first run", st.DaysRemaining, TrialDays)
	}
}

func TestTrialCountsDown(t *testing.T) {
	tr, _, clock, _ := newTrialTracker(t)
	tr.resolve() // start the trial

	clock.advance(5 * 24 * time.Hour)
	if got := tr.resolve().DaysRemaining; got != TrialDays-5 {
		t.Errorf("after 5 days DaysRemaining = %d, want %d", got, TrialDays-5)
	}

	clock.advance(9 * 24 * time.Hour) // total 14 days
	st := tr.resolve()
	if !st.Expired {
		t.Errorf("after %d days trial should be expired, got DaysRemaining=%d", TrialDays, st.DaysRemaining)
	}
}

// TestTrialClockRollbackDoesNotExtend is the anti-cheat core: use 10 days,
// then set the clock back to day 0. The high-water LastSeen must keep elapsed
// where it was — the trial must NOT jump back to ~14 days left.
func TestTrialClockRollbackDoesNotExtend(t *testing.T) {
	tr, _, clock, _ := newTrialTracker(t)
	start := clock.now()
	tr.resolve()

	clock.advance(10 * 24 * time.Hour)
	if got := tr.resolve().DaysRemaining; got != TrialDays-10 {
		t.Fatalf("precondition: after 10 days want %d, got %d", TrialDays-10, got)
	}

	// Roll the clock all the way back to the trial start.
	clock.set(start)
	st := tr.resolve()
	if st.DaysRemaining > TrialDays-10 {
		t.Errorf("clock rollback extended the trial: DaysRemaining=%d, want <= %d", st.DaysRemaining, TrialDays-10)
	}

	// Roll back even further, before the start.
	clock.set(start.Add(-30 * 24 * time.Hour))
	st = tr.resolve()
	if st.DaysRemaining > TrialDays-10 {
		t.Errorf("rollback before start extended the trial: DaysRemaining=%d", st.DaysRemaining)
	}
}

// TestTrialUsesEarliestStartAcrossStores: if the keychain remembers an older
// start than a freshly-written file, the older start wins (a user can't reset
// by deleting the file and letting a new "now" be recorded — the keychain's
// older start is authoritative).
func TestTrialUsesEarliestStartAcrossStores(t *testing.T) {
	tr, kc, clock, dir := newTrialTracker(t)

	// Simulate a keychain record that started 12 days ago, with no file record.
	old := clock.now().Add(-12 * 24 * time.Hour)
	rec := trialRecord{Start: old, LastSeen: old}
	b, _ := json.Marshal(rec)
	_ = kc.Set(keychainService, accountTrial, string(b))

	// Ensure no file record exists.
	_ = os.Remove(filepath.Join(dir, trialFileName))

	st := tr.resolve()
	if st.DaysRemaining != TrialDays-12 {
		t.Errorf("DaysRemaining = %d, want %d (should honor the older keychain start)", st.DaysRemaining, TrialDays-12)
	}
}

// TestTrialSelfHealsDeletedFile: after the trial is established, deleting the
// on-disk file must not reset anything — the keychain copy restores it, and
// resolve rewrites the file.
func TestTrialSelfHealsDeletedFile(t *testing.T) {
	tr, _, clock, dir := newTrialTracker(t)
	tr.resolve()
	clock.advance(8 * 24 * time.Hour)
	tr.resolve()

	// Delete the file half.
	if err := os.Remove(filepath.Join(dir, trialFileName)); err != nil {
		t.Fatalf("remove trial file: %v", err)
	}

	st := tr.resolve()
	if st.DaysRemaining != TrialDays-8 {
		t.Errorf("after deleting the trial file DaysRemaining = %d, want %d", st.DaysRemaining, TrialDays-8)
	}
	// File should be back.
	if _, err := os.Stat(filepath.Join(dir, trialFileName)); err != nil {
		t.Errorf("trial file was not rewritten: %v", err)
	}
}

// TestTrialResolveIsIdempotent: repeated calls with no time passing keep the
// same days remaining (StartTrialIfNeeded relies on this).
func TestTrialResolveIsIdempotent(t *testing.T) {
	tr, _, _, _ := newTrialTracker(t)
	first := tr.resolve().DaysRemaining
	for i := 0; i < 5; i++ {
		if got := tr.resolve().DaysRemaining; got != first {
			t.Fatalf("resolve #%d returned %d, want stable %d", i, got, first)
		}
	}
}

// TestTrialForwardExcursionIsBounded: a single far-future clock reading must
// not bake years into the stored high-water. After the clock returns to
// normal, the persisted LastSeen is at most start+maxForwardStep, not the
// wild future value. (This bounds the stored record; it does NOT make the
// trial un-expire — 45d > the 14-day term — which is the documented residual.)
func TestTrialForwardExcursionIsBounded(t *testing.T) {
	tr, _, clock, dir := newTrialTracker(t)
	start := clock.now()
	tr.resolve() // start trial at day 0

	clock.advance(3 * 24 * time.Hour) // real day 3
	tr.resolve()

	// Clock glitches ~5 years into the future for one resolve.
	clock.set(start.Add(5 * 365 * 24 * time.Hour))
	tr.resolve()

	// Read what got persisted to the file store.
	rec, ok := tr.readFile()
	if !ok {
		t.Fatal("expected a persisted trial record")
	}
	maxAllowed := start.Add(3*24*time.Hour + maxForwardStep + time.Minute)
	if rec.LastSeen.After(maxAllowed) {
		t.Errorf("forward excursion baked a wild future into storage: LastSeen=%v, want <= ~%v", rec.LastSeen, maxAllowed)
	}
	_ = dir
}
