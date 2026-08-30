package license

import "time"

// Licensing state values. Exactly one is ever the resolved State.
const (
	// StateTrial: no license stored, trial still has days left.
	StateTrial = "trial"
	// StateLicensed: a signature-valid license, bound to THIS machine, is
	// stored. This is the terminal happy state and it is reachable OFFLINE
	// forever — see the resolution notes below.
	StateLicensed = "licensed"
	// StateTrialExpired: no license stored, trial is used up.
	StateTrialExpired = "trial_expired"
	// StateLicenseInvalid: a license is stored but it failed verification —
	// its signature is bad/forged, or it is bound to a different machine.
	StateLicenseInvalid = "license_invalid"
)

// Status is the ONE structure the whole app reads to decide what to show and
// (softly) gate. It is produced by Manager.Status and mirrored 1:1 to the
// frontend (frontend/src/lib/license.ts). JSON tags match the TS type.
//
// A deliberate design commitment, load-bearing for the product's "not a
// subscription, never bricked offline" promise:
//
//	A stored license that is signature-valid AND bound to this machine ALWAYS
//	resolves to StateLicensed. Nothing about being offline, our server being
//	down, the updates window lapsing, or the online-recheck grace expiring can
//	turn it into StateLicenseInvalid or force a downgrade. Those conditions
//	set advisory flags (UpdatesExpired, InGrace/GraceDaysRemaining) that the UI
//	may surface, but they never revoke a paid, valid license. Only a BAD
//	SIGNATURE or a WRONG-MACHINE binding yields StateLicenseInvalid.
type Status struct {
	State string `json:"state"`

	// DaysRemaining is trial days left when State is StateTrial; 0 otherwise.
	DaysRemaining int `json:"daysRemaining"`

	// Identity + seat info, populated when a license is stored (licensed or
	// invalid — so the UI can say "license for x@y is invalid on this machine").
	Email        string `json:"email,omitempty"`
	Name         string `json:"name,omitempty"`
	Plan         string `json:"plan,omitempty"`
	MachineCount int    `json:"machineCount,omitempty"`
	MaxMachines  int    `json:"maxMachines,omitempty"`

	// MachineID is this machine's current fingerprint — handy for support and
	// for the UI to show which machine it's looking at.
	MachineID string `json:"machineId,omitempty"`

	// UpdatesExpired is true when now is past the license's ExpiresUpdatesAt.
	// The build keeps running (perpetual license); this only tells the UI the
	// "12 months of updates" window has lapsed. Never affects State.
	UpdatesExpired  bool   `json:"updatesExpired,omitempty"`
	UpdatesExpireAt string `json:"updatesExpireAt,omitempty"` // RFC3339, for display

	// InGrace / GraceDaysRemaining describe the OPTIONAL online-recheck grace:
	// InGrace is true while we're within GraceDays of the last successful
	// online validation. When it lapses the license still works offline (State
	// stays StateLicensed) — this is purely advisory for a future "please
	// reconnect to re-verify" nudge. GraceDaysRemaining is 0 once lapsed.
	InGrace            bool `json:"inGrace,omitempty"`
	GraceDaysRemaining int  `json:"graceDaysRemaining,omitempty"`

	// Message is a short human-readable explanation, set for the invalid and
	// expired-updates cases so the UI needn't re-derive the wording.
	Message string `json:"message,omitempty"`
}

// graceState reports whether `now` is still within `grace` of the last
// successful online validation, and how many whole days of grace remain. It
// is a pure function, split out so the grace math is unit-tested directly.
//
// A zero lastValidated (never validated online) is treated as in-grace with
// full days remaining rather than immediately lapsed: we must never nudge —
// let alone gate — a license that simply hasn't had a chance to phone home
// yet, consistent with offline-first.
func graceState(lastValidated, now time.Time, grace time.Duration) (inGrace bool, daysRemaining int) {
	if lastValidated.IsZero() {
		return true, int(grace.Hours() / 24)
	}
	deadline := lastValidated.Add(grace)
	if !now.Before(deadline) {
		return false, 0
	}
	remaining := deadline.Sub(now)
	days := int(remaining.Hours() / 24)
	if days < 1 {
		days = 1 // any time left rounds up to at least a day
	}
	return true, days
}
