package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// devPublicKeyBase64 is the Ed25519 PUBLIC key AUK verifies signed licenses
// against. It is compiled into the binary; only the matching PRIVATE key can
// mint a license this build will accept.
//
// ┌─────────────────────────────────────────────────────────────────────────┐
// │ THIS IS A DEV / TEST KEY.                                                 │
// │                                                                           │
// │ Its private half currently lives in the session scratchpad               │
// │ (auk_license_ed25519.key) so licenses can be minted for local testing    │
// │ with cmd/mklicense. It is deliberately NOT committed.                     │
// │                                                                           │
// │ BEFORE SELLING: generate a FRESH keypair whose private half exists ONLY   │
// │ inside the license-issuing worker (the Lemon Squeezy / Paddle webhook or  │
// │ a small signing service), replace the constant below with that new        │
// │ PUBLIC key, and never let the production private key touch a developer    │
// │ machine, this repo, or CI logs. Anyone holding the private key can mint   │
// │ licenses this app accepts. See docs/06-licensing.md.                      │
// └─────────────────────────────────────────────────────────────────────────┘
const devPublicKeyBase64 = "HzLrTKPV1XtoyQ9WfV1/RyTK2c/O30TtRP1DcHR5SjQ="

// ErrBadSignature is returned by verification when a SignedLicense does not
// carry a valid signature from the expected key. It intentionally does not
// distinguish "tampered" from "signed by the wrong key" from "malformed
// signature" — to an offline verifier those are the same failure: this is not
// a license we can trust.
var ErrBadSignature = errors.New("license: signature verification failed")

// embeddedPublicKey parses the compiled-in verification key. A panic here
// would mean the constant above was corrupted at edit time (a build-breaking
// programming error, not a runtime condition), so it is surfaced as an error
// the caller resolves once at construction rather than being ignored.
func embeddedPublicKey() (ed25519.PublicKey, error) {
	return parsePublicKey(devPublicKeyBase64)
}

func parsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("license: decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKeyBase64 decodes a base64 (standard-encoding) Ed25519 private
// key — the 64-byte form ed25519.GenerateKey returns. Used by cmd/mklicense
// (reading the dev key from the scratchpad) and, later, by the production
// signing worker.
func ParsePrivateKeyBase64(b64 string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("license: decode private key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("license: private key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// Sign mints a SignedLicense by signing lic.canonicalBytes() with priv. This
// is the issuer side — cmd/mklicense and, in production, the MoR webhook. The
// running app never signs, only verifies.
func Sign(priv ed25519.PrivateKey, lic License) (SignedLicense, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return SignedLicense{}, fmt.Errorf("license: private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	sig := ed25519.Sign(priv, lic.canonicalBytes())
	return SignedLicense{
		License:   lic,
		Alg:       "ed25519",
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// Verify checks a SignedLicense against the embedded (production/dev) public
// key. A nil return means the signature is authentic AND covers exactly these
// license fields — it says nothing about whether the license is bound to this
// machine or still within its updates window; those are separate checks in
// status.go. Any tampering with any field, or a signature from any other key,
// yields ErrBadSignature.
func Verify(sl SignedLicense) error {
	pub, err := embeddedPublicKey()
	if err != nil {
		return err
	}
	return verifyWith(pub, sl)
}

// verifyWith is the key-parameterized core of Verify, split out so tests can
// exercise the exact same verification path against an ephemeral keypair
// (no dependency on the embedded key's private half, which never lives in the
// repo) and so the Manager can be pointed at a test key.
func verifyWith(pub ed25519.PublicKey, sl SignedLicense) error {
	sig, err := base64.StdEncoding.DecodeString(sl.Signature)
	if err != nil {
		return ErrBadSignature
	}
	if len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, sl.License.canonicalBytes(), sig) {
		return ErrBadSignature
	}
	return nil
}
