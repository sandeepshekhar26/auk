package license

import (
	"encoding/binary"
	"strconv"
	"time"
)

// TrialDays is the length of the no-account trial. 14 days from first launch.
const TrialDays = 14

// DefaultMaxMachines is the seat count a personal license activates on unless
// the issuer says otherwise (docs/06-licensing.md).
const DefaultMaxMachines = 3

// GraceDays is how long a previously-validated license keeps its "recently
// re-checked online" standing after the last successful server round-trip.
// Note (important, and documented at length in status.go): running AUK never
// requires this — a cryptographically valid, correctly-bound license is
// "licensed" OFFLINE FOREVER. Grace only governs the OPTIONAL online
// revocation re-check, and its expiry is a soft flag, never a hard block.
const GraceDays = 30

// canonicalTag is a domain-separation prefix mixed into every signed payload.
// It ties a signature to "an AUK license, format v1" specifically, so a
// signature can never be lifted and replayed against some other Ed25519
// payload this key might one day also sign (a standard signing-scheme
// precaution). Bump the version suffix only alongside a real format change.
const canonicalTag = "AUK-LICENSE-v1\n"

// License is the set of facts the issuer asserts and signs. It is the
// plaintext half of a SignedLicense; the Signature covers exactly the bytes
// canonicalBytes produces from these fields, in this order.
//
// The struct is serialized to JSON for storage/transport, but JSON field
// ORDER is never trusted for verification — canonicalBytes re-derives the
// signed bytes deterministically and independently of json.Marshal, so
// reordering these fields (or a JSON library reformatting them) cannot
// change what was signed. See canonicalBytes.
type License struct {
	// LicenseKey is the opaque key the Merchant of Record (Lemon Squeezy /
	// Paddle) issued at purchase. AUK treats it as an identifier only — it is
	// never parsed or trusted for anything on its own; trust comes from the
	// signature over this whole struct.
	LicenseKey string `json:"licenseKey"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	// Plan is a free-form tier label from the issuer ("personal", "team", …).
	Plan string `json:"plan"`
	// MachineID is the fingerprint this license is CRYPTOGRAPHICALLY BOUND to.
	// A license minted for machine A will not resolve to "licensed" on machine
	// B even though its signature is perfectly valid — the binding is checked
	// separately from the signature (see status.go). This is what makes a
	// copied license file useless without re-activating on the new machine.
	MachineID string `json:"machineId"`
	// MaxMachines is the seat cap the issuer enforces server-side at
	// activation time. Carried here for display ("N/3") and future client-side
	// sanity only — the authoritative enforcement is the MoR's, since one
	// offline machine cannot know how many others are activated.
	MaxMachines int `json:"maxMachines"`
	// MachineCount is the activation count the server reported AT ISSUE TIME —
	// a snapshot for the "N/3 machines" display, not a live number.
	MachineCount int `json:"machineCount"`
	// IssuedAt is when the issuer signed this license.
	IssuedAt time.Time `json:"issuedAt"`
	// ExpiresUpdatesAt is purchase + 12 months. AUK builds released AFTER this
	// date still RUN on this license (perpetual license — see the product
	// pitch), they simply don't count as "update-activated". Never a reason to
	// downgrade to unlicensed; only a display flag. See status.go.
	ExpiresUpdatesAt time.Time `json:"expiresUpdatesAt"`
}

// canonicalBytes produces the exact, deterministic byte string the signature
// is computed over. THIS is the canonical signing scheme (documented in
// docs/06-licensing.md):
//
//	canonicalTag
//	│ then, for each field below IN THIS FIXED ORDER:
//	│   uint32 big-endian length ‖ raw UTF-8 bytes
//	│
//	1  LicenseKey        (string)
//	2  Email             (string)
//	3  Name              (string)
//	4  Plan              (string)
//	5  MachineID         (string)
//	6  MaxMachines       (decimal string)
//	7  MachineCount      (decimal string)
//	8  IssuedAt          (RFC3339, UTC, second precision)
//	9  ExpiresUpdatesAt  (RFC3339, UTC, second precision)
//
// Two properties make this safe as a signing input:
//
//   - Length-prefixing (not a delimiter) means no field value can ever be
//     confused with a field boundary. Without it, a Name of "a\nEmail=b"
//     could forge a different field layout; here the length says exactly how
//     many bytes Name occupies and parsing/forgery of boundaries is
//     impossible.
//   - Times are always formatted via RFC3339 at SECOND precision, so a JSON
//     round-trip that adds or drops sub-second digits (or a monotonic-clock
//     reading) cannot change the signed bytes — verification re-formats the
//     same way and matches.
func (l License) canonicalBytes() []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, canonicalTag...)

	writeField := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		buf = append(buf, n[:]...)
		buf = append(buf, s...)
	}

	writeField(l.LicenseKey)
	writeField(l.Email)
	writeField(l.Name)
	writeField(l.Plan)
	writeField(l.MachineID)
	writeField(strconv.Itoa(l.MaxMachines))
	writeField(strconv.Itoa(l.MachineCount))
	writeField(l.IssuedAt.UTC().Format(time.RFC3339))
	writeField(l.ExpiresUpdatesAt.UTC().Format(time.RFC3339))

	return buf
}

// SignedLicense is what the issuer hands back and what AUK persists: the
// asserted License plus a detached Ed25519 signature over canonicalBytes.
// This is the only thing that ever needs to travel or be stored — everything
// AUK trusts about a license is re-derivable from it offline.
type SignedLicense struct {
	License License `json:"license"`
	// Alg names the signature algorithm, so a future key/algorithm rotation
	// can be detected rather than silently mis-verified. Always "ed25519" today.
	Alg string `json:"alg"`
	// Signature is base64 (standard encoding) of the 64-byte Ed25519 signature
	// over License.canonicalBytes().
	Signature string `json:"signature"`
}
