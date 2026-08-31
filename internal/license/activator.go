package license

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Activator exchanges an opaque Merchant-of-Record license key for a
// SignedLicense bound to this machine. It is the ONLY part of licensing that
// ever needs the network; everything after activation is offline.
//
// Two implementations exist:
//   - mockActivator signs locally with a private key (tests, and local dev via
//     the dev key). No network.
//   - remoteActivator is the production path: it calls the first-party
//     licence-signing worker (worker/), which validates the key against Paddle
//     and signs a machine-bound licence.
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

// DefaultActivationBaseURL is the licence-signing worker (worker/), served on
// the marketing site's own origin. It is the ONE network endpoint AUK ever
// contacts, and only during activation.
const DefaultActivationBaseURL = "https://auk.deskmcp.com/api"

// activationTimeout bounds the single request. Long enough for a cold Worker
// start on a slow connection, short enough that a hung network shows the user
// an error instead of a spinner that never resolves.
const activationTimeout = 20 * time.Second

// maxActivationResponseBytes caps what a response can cost us. The real
// response is a few hundred bytes; anything approaching this is a
// misconfiguration or a hostile endpoint, and either way must not be read into
// memory unbounded.
const maxActivationResponseBytes = 64 << 10

// remoteActivator exchanges a licence key for a SignedLicense at the
// first-party signing worker.
//
// The trust model is the point of this design and is worth restating: AUK does
// NOT trust this server's answer because the server said so. It trusts the
// Ed25519 signature the response carries, which only the production private key
// can produce, and which Manager.storeVerifiedLocked re-verifies against the
// compiled-in public key before storing anything. A compromised or spoofed
// endpoint can refuse to activate; it cannot mint a licence AUK accepts.
//
// That is also why the worker — not Paddle — is the endpoint. Paddle knows
// whether a key was paid for; only we can sign. The worker asks Paddle, then
// signs. See worker/src/index.js.
type remoteActivator struct {
	// baseURL has no trailing slash. Empty means DefaultActivationBaseURL.
	baseURL string
	// httpClient is optional; nil uses a client with activationTimeout.
	httpClient *http.Client
}

// ErrRemoteActivationUnreachable is returned when the signing worker could not
// be reached at all — offline, DNS failure, TLS failure. It is deliberately
// distinct from a refusal: "we couldn't ask" is a retry-later condition, while
// a refusal is final. Existing licences are unaffected either way; AUK verifies
// offline forever once activated.
var ErrRemoteActivationUnreachable = errors.New(
	"couldn't reach the AUK licence server — check your connection and try again")

// activationRequest is the body posted to the signing worker.
type activationRequest struct {
	LicenseKey  string `json:"licenseKey"`
	Fingerprint string `json:"fingerprint"`
}

// activationError is the worker's error shape. Message is written for the
// person reading it, so it is surfaced verbatim when present; Code is matched
// only where AUK can add something the server cannot know.
type activationError struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (a remoteActivator) endpoint() string {
	base := a.baseURL
	if base == "" {
		base = DefaultActivationBaseURL
	}
	return strings.TrimSuffix(base, "/") + "/v1/licenses/activate"
}

func (a remoteActivator) client() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return &http.Client{Timeout: activationTimeout}
}

// Activate posts the key and this machine's fingerprint and returns the signed
// licence the worker mints. The returned SignedLicense is NOT trusted here —
// the caller verifies it.
func (a remoteActivator) Activate(ctx context.Context, key, fingerprint string) (SignedLicense, error) {
	if strings.TrimSpace(key) == "" {
		return SignedLicense{}, errors.New("license: empty license key")
	}
	if strings.TrimSpace(fingerprint) == "" {
		return SignedLicense{}, errors.New("license: empty machine fingerprint")
	}

	body, err := json.Marshal(activationRequest{LicenseKey: key, Fingerprint: fingerprint})
	if err != nil {
		return SignedLicense{}, fmt.Errorf("license: encode activation request: %w", err)
	}

	// A context bound to the activation timeout even when the caller passes
	// context.Background(), so the UI cannot hang indefinitely on a stalled
	// connection that never triggers the client's own timeout.
	ctx, cancel := context.WithTimeout(ctx, activationTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(body))
	if err != nil {
		return SignedLicense{}, fmt.Errorf("license: build activation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client().Do(req)
	if err != nil {
		// The transport error is wrapped rather than shown: it names hosts and
		// TLS internals that mean nothing to a customer mid-purchase.
		return SignedLicense{}, fmt.Errorf("%w (%v)", ErrRemoteActivationUnreachable, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxActivationResponseBytes))
	if err != nil {
		return SignedLicense{}, fmt.Errorf("%w (%v)", ErrRemoteActivationUnreachable, err)
	}

	if resp.StatusCode != http.StatusOK {
		return SignedLicense{}, activationFailure(resp.StatusCode, payload)
	}

	var signed SignedLicense
	if err := json.Unmarshal(payload, &signed); err != nil {
		return SignedLicense{}, errors.New("the licence server sent a response AUK couldn't read — please contact support")
	}
	if signed.Signature == "" {
		return SignedLicense{}, errors.New("the licence server returned an unsigned licence — please contact support")
	}
	return signed, nil
}

// activationFailure turns a non-200 into the sentence the user should read.
//
// The worker already writes customer-facing messages, so those are preferred.
// The fallbacks exist for the cases the worker never reaches: a proxy, a
// captive portal, or a Cloudflare error page standing in for it.
func activationFailure(status int, payload []byte) error {
	var apiErr activationError
	_ = json.Unmarshal(payload, &apiErr)
	if apiErr.Message != "" {
		return errors.New(apiErr.Message)
	}
	switch {
	case status == http.StatusNotFound:
		return errors.New("we don't recognise that licence key — check it for typos, or paste the licence file from your email")
	case status == http.StatusConflict:
		return errors.New("that licence is already active on the maximum number of Macs — deactivate one, or contact support")
	case status == http.StatusForbidden:
		return errors.New("that licence is no longer valid — please contact support")
	case status >= 500:
		return fmt.Errorf("%w (server error %d)", ErrRemoteActivationUnreachable, status)
	default:
		return fmt.Errorf("activation failed (%d) — please contact support", status)
	}
}

// Deactivate releases this machine's seat so the licence can be activated on
// another Mac. It is best-effort BY DESIGN: local deactivation must always
// succeed, because a user who is offline, or travelling, or wiping the machine
// still needs to be able to remove their licence. A failure here leaves a stale
// seat that support can clear, which is far better than refusing to deactivate.
func (a remoteActivator) Deactivate(ctx context.Context, key, fingerprint string) error {
	body, err := json.Marshal(activationRequest{LicenseKey: key, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, activationTimeout)
	defer cancel()

	base := a.baseURL
	if base == "" {
		base = DefaultActivationBaseURL
	}
	url := strings.TrimSuffix(base, "/") + "/v1/licenses/deactivate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrRemoteActivationUnreachable, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxActivationResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("licence server refused the deactivation (%d)", resp.StatusCode)
	}
	return nil
}

// seatReleaser is the optional half of Activator: an activator that can also
// free a seat server-side. Manager.Deactivate calls it when present and ignores
// the result (see the honesty note on Deactivate above). Keeping it OUT of the
// Activator interface means mockActivator and every test double stay a
// one-method implementation.
type seatReleaser interface {
	Deactivate(ctx context.Context, key, fingerprint string) error
}
