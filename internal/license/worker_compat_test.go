package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The licence-signing worker (worker/) re-implements canonicalBytes in
// JavaScript, because Cloudflare Workers cannot run this Go code. That
// duplication is the single most dangerous seam in the product: if the two
// encoders disagree by ONE byte, every licence sold is rejected by the app as
// a forgery, and the only symptom is "signature verification failed" — which
// looks identical to an attack and says nothing about an encoder drifting.
//
// Reviewing the two files side by side is not enough, so these tests run the
// real JavaScript signer and verify its output with the real Go verifier.
// Anything that changes the signing scheme on either side fails here.

// runWorkerSigner signs lic with priv by invoking the worker's own JS modules.
func runWorkerSigner(t *testing.T, priv ed25519.PrivateKey, lic License) SignedLicense {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH — skipping the Go/JS licence-encoding cross-check")
	}

	// The times are handed over as RFC3339 with sub-second digits ON PURPOSE:
	// a real Paddle payload carries millisecond timestamps, and the JS side
	// must truncate them exactly as Go's second-precision formatting does. If
	// it did not, licences would verify in the worker's own tests and fail on
	// the customer's Mac.
	payload, err := json.Marshal(map[string]any{
		"licenseKey":       lic.LicenseKey,
		"email":            lic.Email,
		"name":             lic.Name,
		"plan":             lic.Plan,
		"machineId":        lic.MachineID,
		"maxMachines":      lic.MaxMachines,
		"machineCount":     lic.MachineCount,
		"issuedAt":         lic.IssuedAt.UTC().Format(time.RFC3339Nano),
		"expiresUpdatesAt": lic.ExpiresUpdatesAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	script := filepath.Join("..", "..", "worker", "scripts", "sign-fixture.mjs")
	out, err := exec.Command(node, script,
		base64.StdEncoding.EncodeToString(priv), string(payload)).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("worker signer failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("worker signer failed: %v", err)
	}

	var signed SignedLicense
	if err := json.Unmarshal(out, &signed); err != nil {
		t.Fatalf("unmarshal worker output %q: %v", out, err)
	}
	return signed
}

func compatFixture() License {
	return License{
		LicenseKey:   "AUK-4KDR2-8QW1M-VZ0PT-N7C93",
		Email:        "buyer@example.com",
		Name:         "A Buyer",
		Plan:         "personal",
		MachineID:    "hw-fixturemachine00000000000000",
		MaxMachines:  3,
		MachineCount: 1,
		// Deliberately NOT on a second boundary.
		IssuedAt:         time.Date(2026, 8, 31, 10, 4, 5, 937_000_000, time.UTC),
		ExpiresUpdatesAt: time.Date(2027, 8, 31, 10, 4, 5, 937_000_000, time.UTC),
	}
}

// TestWorkerSignedLicenseVerifiesInGo is the contract test: a licence minted
// by the JavaScript worker must verify with the Go verifier that ships in the
// app, over a JSON round trip.
func TestWorkerSignedLicenseVerifiesInGo(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	lic := compatFixture()
	signed := runWorkerSigner(t, priv, lic)

	if signed.Alg != "ed25519" {
		t.Fatalf("alg = %q, want ed25519", signed.Alg)
	}
	if err := verifyWith(pub, signed); err != nil {
		t.Fatalf("Go rejected a licence the worker signed: %v", err)
	}
}

// TestWorkerAndGoProduceIdenticalSignatures is the stronger statement.
// Ed25519 is deterministic, so identical inputs must yield the identical
// signature — meaning the two canonical encoders produced the identical bytes.
// A mismatch here localises the bug to the ENCODING, not to key handling.
func TestWorkerAndGoProduceIdenticalSignatures(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	lic := compatFixture()

	fromWorker := runWorkerSigner(t, priv, lic)

	// Go signs the second-truncated licence, because that is what canonical-
	// Bytes signs regardless of the sub-second digits carried in the struct.
	fromGo, err := Sign(priv, lic)
	if err != nil {
		t.Fatalf("Go sign: %v", err)
	}

	if fromWorker.Signature != fromGo.Signature {
		t.Fatalf("canonical encodings have DRIFTED — worker and app disagree.\n"+
			"worker: %s\napp:    %s\n"+
			"Compare worker/src/canonical.js with License.canonicalBytes in model.go.",
			fromWorker.Signature, fromGo.Signature)
	}
}

// TestWorkerLicenseJSONRoundTripsIntoGoModel proves the worker's field names
// and value shapes match the app's json tags — a licence that verifies but
// deserialises into empty fields would still be broken.
func TestWorkerLicenseJSONRoundTripsIntoGoModel(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	lic := compatFixture()
	got := runWorkerSigner(t, priv, lic).License

	want := lic
	// The worker signs and emits whole seconds; that IS the licence.
	want.IssuedAt = lic.IssuedAt.UTC().Truncate(time.Second)
	want.ExpiresUpdatesAt = lic.ExpiresUpdatesAt.UTC().Truncate(time.Second)

	if got.LicenseKey != want.LicenseKey || got.Email != want.Email ||
		got.Name != want.Name || got.Plan != want.Plan || got.MachineID != want.MachineID ||
		got.MaxMachines != want.MaxMachines || got.MachineCount != want.MachineCount ||
		!got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresUpdatesAt.Equal(want.ExpiresUpdatesAt) {
		t.Fatalf("worker licence JSON did not round-trip into the Go model.\ngot:  %+v\nwant: %+v", got, want)
	}
}
