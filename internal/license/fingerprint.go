package license

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Fingerprinter derives a stable per-machine identifier. The identifier is
// what a license is bound to, so its two jobs are: be the SAME value on every
// launch of AUK on this Mac, and be DIFFERENT on a different Mac.
//
// Resolution order (see MachineID):
//  1. Hardware-derived: the macOS IOPlatformUUID (via ioreg). This is stable
//     across reinstalls, app-data wipes, and even a full OS reinstall on the
//     same hardware — the strongest binding, and it needs no persistence.
//  2. Fallback: a random UUID generated once and stored in the OS keychain,
//     used only when the hardware id can't be read (a non-macOS host, or ioreg
//     unavailable). Stable across launches; resets only if the keychain entry
//     is removed.
//
// The raw hardware UUID is never placed in the license — MachineID returns a
// SHA-256-derived, prefixed short hash of it, so binding is reproducible
// without embedding a hardware serial into a file the user might share.
type Fingerprinter struct {
	kc Keychain
	// hostID returns a raw hardware-stable string and true, or ("", false)
	// when none is available. Injected so tests are independent of the real
	// host's hardware id. Defaults to macHardwareUUID.
	hostID func() (string, bool)
}

// NewFingerprinter builds the production Fingerprinter (macOS hardware UUID,
// keychain fallback).
func NewFingerprinter(kc Keychain) *Fingerprinter {
	return &Fingerprinter{kc: kc, hostID: macHardwareUUID}
}

// MachineID returns this machine's stable fingerprint, resolving and
// persisting it. It is designed so that a machine which has EVER successfully
// read its hardware id keeps returning that same "hw-…" id forever, even if a
// later hardware read momentarily fails — because a fingerprint that silently
// changes would flip a genuinely valid, paid license to "wrong machine" and
// block the user, the one thing the licensing model forbids (see manager.go).
//
// Resolution:
//  1. Hardware readable → "hw-<hash>", and persist it (only on change) so a
//     future failed read can reuse it instead of minting a different id.
//  2. Hardware NOT readable → reuse the persisted id. On any Mac that has ever
//     launched AUK successfully this is the hw- id from step 1, so a transient
//     ioreg hiccup (fd pressure, a future ioreg format change, PATH quirk)
//     can never switch the machine into a different fingerprint namespace.
//  3. Nothing persisted AND hardware unreadable (true first run on a host with
//     no readable hardware id) → mint a stable "gen-<uuid>" fallback.
func (f *Fingerprinter) MachineID() (string, error) {
	if raw, ok := f.hostID(); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			id := "hw-" + shortHash(trimmed)
			// Persist the hardware id so step 2 can reuse it on a later read
			// failure. Write only when it differs from what's stored, to avoid
			// needless keychain churn on every launch. A write failure is
			// non-fatal — the id is still correct for this run; the risk it
			// guards against (a future read failure) simply isn't covered until
			// a write succeeds.
			if cur, err := f.kc.Get(keychainService, accountFingerprint); err != nil || strings.TrimSpace(cur) != id {
				_ = f.kc.Set(keychainService, accountFingerprint, id)
			}
			return id, nil
		}
	}

	// Hardware unreadable: reuse a previously resolved id (normally the hw- id
	// persisted above) rather than switching namespaces.
	if id, err := f.kc.Get(keychainService, accountFingerprint); err == nil {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			return trimmed, nil
		}
	}
	// True first run with no readable hardware id: mint a stable fallback and
	// persist it so it's the same next launch. A keychain write failure is
	// non-fatal — a usable id for this run beats resolving none.
	id := "gen-" + uuid.NewString()
	_ = f.kc.Set(keychainService, accountFingerprint, id)
	return id, nil
}

// shortHash returns the first 16 bytes (32 hex chars) of SHA-256(s) — enough
// to make collisions between real machines astronomically unlikely while
// keeping the fingerprint compact and free of the raw hardware serial.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

var ioplatformUUIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

// macHardwareUUID reads the IOPlatformUUID from ioreg. Returns ("", false) on
// any failure (not macOS, ioreg missing, unparseable output) so the caller
// falls through to the persisted/keychain-backed id.
//
// The absolute path is tried first so the read doesn't depend on $PATH
// containing /usr/sbin (a launchd/GUI context can differ from a shell), with a
// bare "ioreg" retry in case a future macOS relocates it. Combined with
// MachineID persisting the hw- id, a one-off failure here can never change a
// machine's fingerprint.
func macHardwareUUID() (string, bool) {
	for _, bin := range []string{"/usr/sbin/ioreg", "ioreg"} {
		out, err := exec.Command(bin, "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			continue
		}
		if m := ioplatformUUIDRe.FindSubmatch(out); m != nil {
			return string(m[1]), true
		}
	}
	return "", false
}
