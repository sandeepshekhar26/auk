package license

import (
	"strings"
	"testing"
)

// TestFingerprintStableFromHardware: with a hardware id available, MachineID
// is deterministic across calls and derives from (not equal to) the raw id.
func TestFingerprintStableFromHardware(t *testing.T) {
	kc := newFakeKeychain()
	fp := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return "RAW-HW-UUID-1234", true }}

	id1, err := fp.MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	id2, _ := fp.MachineID()
	if id1 != id2 {
		t.Errorf("fingerprint not stable: %q vs %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "hw-") {
		t.Errorf("hardware fingerprint should be hw- prefixed, got %q", id1)
	}
	if strings.Contains(id1, "RAW-HW-UUID-1234") {
		t.Error("fingerprint leaked the raw hardware id instead of hashing it")
	}
	// The hardware path MUST persist the resolved hw- id (so a later hardware
	// read failure can reuse it instead of minting a different one — see
	// TestFingerprintSurvivesHardwareReadFailure).
	stored, err := kc.Get(keychainService, accountFingerprint)
	if err != nil {
		t.Fatalf("hardware id was not persisted: %v", err)
	}
	if stored != id1 {
		t.Errorf("persisted id %q != returned hw id %q", stored, id1)
	}
}

// TestFingerprintSurvivesHardwareReadFailure is the regression test for the
// "brick a paying user" bug: once the hardware id has been resolved and
// persisted, a subsequent hardware read failure must return the SAME id, not a
// fresh gen- id in a different namespace (which would flip a valid license to
// "wrong machine"). See internal/license/fingerprint.go MachineID.
func TestFingerprintSurvivesHardwareReadFailure(t *testing.T) {
	kc := newFakeKeychain()
	hwOK := true
	hostID := func() (string, bool) {
		if hwOK {
			return "RAW-HW-UUID-STABLE", true
		}
		return "", false // simulate a transient ioreg failure
	}
	fp := &Fingerprinter{kc: kc, hostID: hostID}

	// First launch: hardware readable → hw- id, persisted.
	hwID, err := fp.MachineID()
	if err != nil {
		t.Fatalf("MachineID (hw available): %v", err)
	}
	if !strings.HasPrefix(hwID, "hw-") {
		t.Fatalf("expected hw- id, got %q", hwID)
	}

	// Later launch: ioreg momentarily fails. The fingerprint MUST NOT change —
	// otherwise a paid license bound to hwID would read as license_invalid.
	hwOK = false
	afterFail, err := fp.MachineID()
	if err != nil {
		t.Fatalf("MachineID (hw failed): %v", err)
	}
	if afterFail != hwID {
		t.Errorf("fingerprint changed after a transient hardware read failure: %q -> %q (this would brick a paying user)", hwID, afterFail)
	}
	if strings.HasPrefix(afterFail, "gen-") {
		t.Error("fell back to a gen- id despite having a persisted hw- id — the exact namespace flip that invalidates a valid license")
	}

	// Hardware recovers: still the same id.
	hwOK = true
	recovered, _ := fp.MachineID()
	if recovered != hwID {
		t.Errorf("fingerprint changed after hardware recovered: %q -> %q", hwID, recovered)
	}
}

// TestFingerprintDistinctPerMachine: two different hardware ids yield two
// different fingerprints.
func TestFingerprintDistinctPerMachine(t *testing.T) {
	kc := newFakeKeychain()
	a := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return "HW-A", true }}
	b := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return "HW-B", true }}
	ida, _ := a.MachineID()
	idb, _ := b.MachineID()
	if ida == idb {
		t.Errorf("different machines produced the same fingerprint %q", ida)
	}
}

// TestFingerprintFallbackPersists: with no hardware id, MachineID generates a
// random id, persists it to the keychain, and returns the same value next call.
func TestFingerprintFallbackPersists(t *testing.T) {
	kc := newFakeKeychain()
	fp := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return "", false }}

	id1, err := fp.MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	if !strings.HasPrefix(id1, "gen-") {
		t.Errorf("fallback fingerprint should be gen- prefixed, got %q", id1)
	}
	stored, err := kc.Get(keychainService, accountFingerprint)
	if err != nil {
		t.Fatalf("fallback id was not persisted: %v", err)
	}
	if stored != id1 {
		t.Errorf("persisted id %q != returned id %q", stored, id1)
	}

	// A fresh Fingerprinter over the same keychain must reuse it.
	fp2 := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return "", false }}
	id2, _ := fp2.MachineID()
	if id2 != id1 {
		t.Errorf("fallback fingerprint not stable across instances: %q vs %q", id1, id2)
	}
}
