package license

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"
)

// Activator exchanges an opaque Merchant-of-Record license key for a
// SignedLicense bound to this machine. It is the ONLY part of licensing that
// ever needs the network; everything after activation is offline.
//
// Two implementations exist:
//   - mockActivator signs locally with a private key (tests, and local dev via
//     the dev key). No network.
//   - remoteActivator (STUB) is where the real Lemon Squeezy / Paddle call
//     will live. It does not invent an endpoint — see its doc comment.
type Activator interface {
	// Activate validates key for machine `fingerprint` and returns a signed
	// license bound to it. Implementations must set the returned license's
	// MachineID to `fingerprint`.
	Activate(ctx context.Context, key, fingerprint string) (SignedLicense, error)
}

// mockActivator signs a license locally. In tests it is constructed with an
// ephemeral keypair whose public half the Manager is pointed at; for local
// dev it can be constructed with the dev private key so the embedded public
// key verifies it. It performs no key lookup — any non-empty key "succeeds",
// which is exactly what you want for offline testing.
type mockActivator struct {
	priv        ed25519.PrivateKey
	name        string
	email       string
	plan        string
	maxMachines int
	// updatesValidFor is how long the updates window runs from issue. Zero
	// means the default 12 months.
	updatesValidFor time.Duration
	now             func() time.Time
}

func (m mockActivator) Activate(_ context.Context, key, fingerprint string) (SignedLicense, error) {
	if key == "" {
		return SignedLicense{}, errors.New("license: empty license key")
	}
	now := m.now().UTC()
	updates := m.updatesValidFor
	if updates == 0 {
		updates = 365 * 24 * time.Hour
	}
	maxMachines := m.maxMachines
	if maxMachines == 0 {
		maxMachines = DefaultMaxMachines
	}
	lic := License{
		LicenseKey:       key,
		Email:            m.email,
		Name:             m.name,
		Plan:             m.plan,
		MachineID:        fingerprint,
		MaxMachines:      maxMachines,
		MachineCount:     1,
		IssuedAt:         now,
		ExpiresUpdatesAt: now.Add(updates),
	}
	return Sign(m.priv, lic)
}

// remoteActivator is the PRODUCTION activation path — currently a deliberate
// stub. It does not guess at an API shape; the concrete request/response is
// filled in once the Merchant of Record (Lemon Squeezy vs Paddle) is chosen.
//
// TODO(licensing): implement against the chosen MoR. The intended shape:
//
//	POST {baseURL}/v1/licenses/activate            (MoR's own validate/activate API)
//	Headers: Authorization/Bearer as the MoR requires
//	Request  JSON:  { "license_key": key,
//	                  "instance_name": fingerprint }   // MoR "instance"/"activation" = one machine
//	Response JSON (on success): the MoR confirms the key is valid, not
//	                  refunded/expired, and under its seat cap, and returns
//	                  the customer's email/name/plan + current activation count.
//
// CRUCIAL: the MoR response is NOT what AUK trusts to run offline. AUK trusts
// only a SignedLicense signed by OUR Ed25519 key. So the real flow is:
//
//	app ──key,fingerprint──▶ OUR signing worker ──validates with MoR──▶ MoR
//	app ◀──SignedLicense (our key)── OUR signing worker
//
// i.e. this call actually targets a small first-party worker (the same code
// as cmd/mklicense, minus the manual key file) that (1) verifies the key with
// the MoR, (2) enforces the ≤MaxMachines seat cap, and (3) signs a License
// with the PRODUCTION private key. That worker is the only place the private
// key ever exists. baseURL therefore points at that worker, not directly at
// the MoR. See docs/06-licensing.md.
type remoteActivator struct {
	baseURL string
	// httpClient would be *http.Client; omitted from the stub so it compiles
	// with zero deps and no dead field. Add it when implementing.
}

// ErrRemoteActivationNotConfigured is returned by the stub so the UI shows a
// clear, honest message rather than a confusing transport error. Until the
// MoR worker exists, users activate by pasting a signed license file produced
// by cmd/mklicense (offline activation), which the Manager also accepts.
var ErrRemoteActivationNotConfigured = errors.New(
	"online activation isn't wired up yet — paste a signed license file to activate offline")

func (remoteActivator) Activate(_ context.Context, _, _ string) (SignedLicense, error) {
	// TODO(licensing): replace with the real signing-worker round trip above.
	return SignedLicense{}, ErrRemoteActivationNotConfigured
}
