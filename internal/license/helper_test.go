package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"
)

// fakeKeychain is an in-memory Keychain for tests — no real OS keychain (which
// can pop a permission dialog and would make tests non-hermetic). Mirrors the
// storage package's fake, including returning an error on a miss so callers
// exercise their "not found → absent" branches.
type fakeKeychain struct {
	mu     sync.Mutex
	values map[string]string
	// failWrites, when set, makes Set return an error — used to prove
	// best-effort persistence paths degrade gracefully.
	failWrites bool
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{values: map[string]string{}}
}

func (f *fakeKeychain) k(service, account string) string { return service + "\x00" + account }

func (f *fakeKeychain) Get(service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[f.k(service, account)]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (f *fakeKeychain) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWrites {
		return ErrSecretNotFound // any non-nil error
	}
	f.values[f.k(service, account)] = value
	return nil
}

func (f *fakeKeychain) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, f.k(service, account))
	return nil
}

// fakeClock is a manually-advanced clock so trial/grace time can be driven
// deterministically.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testEnv bundles a Manager wired entirely to fakes, plus the handles a test
// needs to poke at storage, the clock, and the signing key.
type testEnv struct {
	m         *Manager
	kc        *fakeKeychain
	dir       string
	priv      ed25519.PrivateKey // matches m.verifyKey — the mock activator signs with this
	pub       ed25519.PublicKey
	clock     *fakeClock
	machineID string // this fake machine's resolved fingerprint
}

// hostA is the fixed hardware id the default test fingerprinter reports.
const hostA = "TEST-HARDWARE-UUID-A"

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvAt(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
}

func newTestEnvAt(t *testing.T, start time.Time) *testEnv {
	t.Helper()
	dir := t.TempDir()
	kc := newFakeKeychain()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test keypair: %v", err)
	}
	clock := &fakeClock{t: start}
	fp := &Fingerprinter{kc: kc, hostID: func() (string, bool) { return hostA, true }}
	act := mockActivator{
		priv:  priv,
		email: "buyer@example.com",
		name:  "Buyer",
		plan:  "personal",
		now:   clock.now,
	}
	m := newManagerWith(dir, kc, fp, act, pub, clock.now)
	mid, err := fp.MachineID()
	if err != nil {
		t.Fatalf("resolve test machine id: %v", err)
	}
	return &testEnv{m: m, kc: kc, dir: dir, priv: priv, pub: pub, clock: clock, machineID: mid}
}

// signFor mints a license signed with the env's test key (verifiable by the
// Manager) bound to the given machine id.
func (e *testEnv) signFor(machineID string, mutate func(*License)) SignedLicense {
	now := e.clock.now().UTC()
	lic := License{
		LicenseKey:       "AUK-TEST-0001",
		Email:            "buyer@example.com",
		Name:             "Buyer",
		Plan:             "personal",
		MachineID:        machineID,
		MaxMachines:      DefaultMaxMachines,
		MachineCount:     1,
		IssuedAt:         now,
		ExpiresUpdatesAt: now.Add(365 * 24 * time.Hour),
	}
	if mutate != nil {
		mutate(&lic)
	}
	sl, err := Sign(e.priv, lic)
	if err != nil {
		panic(err)
	}
	return sl
}
