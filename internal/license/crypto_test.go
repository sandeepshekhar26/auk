package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func sampleLicense() License {
	iss := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	return License{
		LicenseKey:       "AUK-ABCD-EF01-2345-6789",
		Email:            "buyer@example.com",
		Name:             "A. Buyer",
		Plan:             "personal",
		MachineID:        "hw-deadbeefdeadbeefdeadbeefdeadbeef",
		MaxMachines:      3,
		MachineCount:     1,
		IssuedAt:         iss,
		ExpiresUpdatesAt: iss.AddDate(1, 0, 0),
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sl, err := Sign(priv, sampleLicense())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sl.Alg != "ed25519" {
		t.Errorf("Alg = %q, want ed25519", sl.Alg)
	}
	if err := verifyWith(pub, sl); err != nil {
		t.Fatalf("verify of freshly signed license failed: %v", err)
	}
}

func TestVerifyRejectsTamperedFields(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sl, _ := Sign(priv, sampleLicense())

	// Every field is covered by the signature: mutating any one must break it.
	cases := map[string]func(*License){
		"email":        func(l *License) { l.Email = "attacker@evil.com" },
		"licenseKey":   func(l *License) { l.LicenseKey = "AUK-0000-0000-0000-0000" },
		"machineId":    func(l *License) { l.MachineID = "hw-someoneelsesmachineaaaaaaaaaaaa" },
		"maxMachines":  func(l *License) { l.MaxMachines = 9999 },
		"machineCount": func(l *License) { l.MachineCount = 0 },
		"plan":         func(l *License) { l.Plan = "enterprise" },
		"expiresAt":    func(l *License) { l.ExpiresUpdatesAt = l.ExpiresUpdatesAt.AddDate(50, 0, 0) },
		"issuedAt":     func(l *License) { l.IssuedAt = l.IssuedAt.AddDate(-1, 0, 0) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			tampered := sl
			tampered.License = sampleLicense()
			mut(&tampered.License)
			if err := verifyWith(pub, tampered); err == nil {
				t.Errorf("tampering %q was NOT rejected", name)
			}
		})
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sl, _ := Sign(priv, sampleLicense())

	// Flip a byte in the signature.
	garbled := sl
	garbled.Signature = "AA" + sl.Signature[2:]
	if err := verifyWith(pub, garbled); err == nil {
		t.Error("garbled signature was not rejected")
	}

	// Empty / non-base64 signatures must be rejected, not panic.
	for _, bad := range []string{"", "not-base64-!!!", "AAAA"} {
		b := sl
		b.Signature = bad
		if err := verifyWith(pub, b); err == nil {
			t.Errorf("bad signature %q was not rejected", bad)
		}
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader) // a different keypair
	sl, _ := Sign(priv, sampleLicense())
	if err := verifyWith(otherPub, sl); err == nil {
		t.Error("license signed by one key verified under a different key")
	}
}

// TestCanonicalBytesStableAcrossJSON is the property the whole scheme leans on:
// a license marshaled to JSON and read back (which is exactly what storage and
// activation do) must produce byte-identical canonical bytes, so its signature
// still verifies. This specifically guards the time-precision hazard — the
// marshaled JSON carries sub-second precision that the canonical encoding must
// normalize away.
func TestCanonicalBytesStableAcrossJSON(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	lic := sampleLicense()
	lic.IssuedAt = time.Date(2026, 1, 15, 9, 30, 0, 123456789, time.UTC) // sub-second
	sl, _ := Sign(priv, lic)

	b, err := json.Marshal(sl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded SignedLicense
	if err := json.Unmarshal(b, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := verifyWith(pub, reloaded); err != nil {
		t.Fatalf("signature broke across a JSON round trip: %v", err)
	}
}

// TestEmbeddedPublicKeyValid guards the compiled-in constant: it must parse to
// a 32-byte Ed25519 key or every real activation would fail.
func TestEmbeddedPublicKeyValid(t *testing.T) {
	pub, err := embeddedPublicKey()
	if err != nil {
		t.Fatalf("embedded public key invalid: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("embedded public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
}
