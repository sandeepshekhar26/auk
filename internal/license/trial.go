package license

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"
)

// trialFileName is the on-disk half of the trial record, kept in the app data
// dir (~/.auk) alongside settings. A leading dot keeps it out of a casual
// directory listing — mild friction, not an anti-feature: the record also
// lives in the OS keychain, and the two are reconciled defensively (see
// resolveTrial), so deleting this one file does not reset the trial.
const trialFileName = ".trial"

// maxForwardStep bounds how far a single live-clock reading may advance the
// trial's high-water mark in one resolve (see resolve). It is deliberately
// larger than any realistic gap between launches so normal use — including a
// weeks-long absence — still accrues real elapsed time and expires the trial
// on schedule, while a far-future clock excursion writes at most this much
// into storage instead of years, keeping the persisted record sane.
const maxForwardStep = 45 * 24 * time.Hour

// trialRecord is the persisted trial state, stored identically in the keychain
// and in the trial file.
//
//   - Start is when the trial began. It only ever moves EARLIER (we keep the
//     earliest value ever seen across both stores), never later, so neither
//     deleting one store nor writing a fresh "now" can push the start forward
//     to buy more time.
//   - LastSeen is a monotonic high-water mark of the latest wall-clock time
//     AUK has ever observed. It only moves FORWARD. It is what defeats a
//     clock rollback: elapsed time is measured against this, so setting the
//     system clock back cannot shrink how much trial has been used.
type trialRecord struct {
	Start    time.Time `json:"start"`
	LastSeen time.Time `json:"lastSeen"`
}

// trialTracker resolves and persists the trial across its two backing stores.
type trialTracker struct {
	kc      Keychain
	dataDir string
	now     func() time.Time
}

// trialState is the resolved, display-ready trial standing.
type trialState struct {
	Start         time.Time
	DaysRemaining int
	Expired       bool
}

func (t *trialTracker) trialFilePath() string {
	return filepath.Join(t.dataDir, trialFileName)
}

// resolve reads both stores, reconciles them defensively, persists the
// reconciled record back to both, and returns the trial standing. It is
// idempotent and safe to call on every launch and every status check:
// repeated calls never extend the trial (Start only moves earlier, LastSeen
// only moves forward). First ever call records the trial start as "now".
//
// This one method backs StartTrialIfNeeded (which just discards the returned
// state) and the trial branch of status resolution, so there is a single
// place trial time is interpreted.
func (t *trialTracker) resolve() trialState {
	now := t.now().UTC()

	fileRec, hasFile := t.readFile()
	kcRec, hasKC := t.readKeychain()

	// Start = earliest non-zero start ever recorded in either store. If
	// neither store has one, this is genuine first run: start now.
	start := time.Time{}
	for _, rec := range []struct {
		rec trialRecord
		ok  bool
	}{{fileRec, hasFile}, {kcRec, hasKC}} {
		if !rec.ok || rec.rec.Start.IsZero() {
			continue
		}
		if start.IsZero() || rec.rec.Start.Before(start) {
			start = rec.rec.Start.UTC()
		}
	}
	if start.IsZero() {
		start = now
	}

	// LastSeen = monotonic high-water mark across both stores and the current
	// clock — advancing it only forward is what stops a clock ROLLBACK from
	// extending the trial (the property that actually matters for a soft gate).
	//
	// The one refinement over a naive high-water: the *live* clock may advance
	// the mark by at most maxForwardStep in a single resolve. This bounds what
	// a transient far-future clock excursion writes into storage — start+45d
	// rather than some year in 2040 — which keeps the persisted record sane and
	// caps the elapsed value the rest of this function sees. It does NOT fully
	// undo a forward glitch: 45d already exceeds the 14-day term, so a machine
	// whose clock jumps well past the trial length still reads as expired (a
	// genuinely-elapsed trial should). Fully distinguishing "the clock really
	// advanced" from "the clock glitched forward then corrected" needs a
	// trusted time source we don't have; for this soft, resettable gate the
	// bounded high-water is the proportionate defense. See docs/06-licensing.md.
	// (Rollback defense is untouched: stored past markers are trusted in full.)
	storedHigh := start
	if hasFile && fileRec.LastSeen.After(storedHigh) {
		storedHigh = fileRec.LastSeen.UTC()
	}
	if hasKC && kcRec.LastSeen.After(storedHigh) {
		storedHigh = kcRec.LastSeen.UTC()
	}
	cappedNow := now
	if maxAdvance := storedHigh.Add(maxForwardStep); now.After(maxAdvance) {
		cappedNow = maxAdvance
	}
	lastSeen := storedHigh
	if cappedNow.After(lastSeen) {
		lastSeen = cappedNow
	}
	if lastSeen.Before(start) {
		lastSeen = start
	}

	reconciled := trialRecord{Start: start, LastSeen: lastSeen}
	// Persist to BOTH stores so a later deletion of either one is self-healed
	// from the survivor. Best-effort: a write failure (e.g. locked keychain)
	// must not break trial resolution for this run.
	t.writeFile(reconciled)
	t.writeKeychain(reconciled)

	// Elapsed is measured against the high-water mark, never the (possibly
	// rolled-back) current clock. Never negative.
	elapsed := lastSeen.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}

	remaining := time.Duration(TrialDays)*24*time.Hour - elapsed
	if remaining <= 0 {
		return trialState{Start: start, DaysRemaining: 0, Expired: true}
	}
	// Round UP so a user with any time left today sees at least "1 day",
	// and a brand-new trial shows the full TrialDays.
	days := int(math.Ceil(remaining.Hours() / 24))
	return trialState{Start: start, DaysRemaining: days, Expired: false}
}

func (t *trialTracker) readFile() (trialRecord, bool) {
	b, err := os.ReadFile(t.trialFilePath())
	if err != nil {
		return trialRecord{}, false
	}
	var rec trialRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return trialRecord{}, false
	}
	return rec, true
}

func (t *trialTracker) writeFile(rec trialRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := os.MkdirAll(t.dataDir, 0o755); err != nil {
		return
	}
	// Write-temp-then-rename so a crash mid-write can't leave a half-written
	// (unparseable) record; the keychain copy would cover it anyway, but this
	// keeps the file store trustworthy on its own.
	tmp := t.trialFilePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, t.trialFilePath())
}

func (t *trialTracker) readKeychain() (trialRecord, bool) {
	v, err := t.kc.Get(keychainService, accountTrial)
	if err != nil || v == "" {
		return trialRecord{}, false
	}
	var rec trialRecord
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		return trialRecord{}, false
	}
	return rec, true
}

func (t *trialTracker) writeKeychain(rec trialRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = t.kc.Set(keychainService, accountTrial, string(b))
}
