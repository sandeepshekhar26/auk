package license

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// Keychain is the minimal OS-credential-vault contract the license package
// needs. It intentionally mirrors internal/storage.SecretStore (Get/Set/
// Delete) but is declared HERE, and backed by the same go-keyring library, so
// the license package stays self-contained and unit-testable (tests inject an
// in-memory fake) without reaching into storage's unexported internals or
// taking a dependency on the FileStore.
//
// A Get miss returns an error (mirroring go-keyring's ErrNotFound); callers
// treat "not found" as a normal absent-value state, not a failure.
type Keychain interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

// keychainService is the go-keyring "service" all AUK license entries live
// under. It shares the "apitool" service name with environment secrets, but
// the accounts below are namespaced so they can never collide with the
// "<workspace>/<env>/<var>" accounts FileStore uses for secret variables.
const keychainService = "apitool"

const (
	// accountLicense stores the JSON storedLicense blob (signed license +
	// activation bookkeeping).
	accountLicense = "auk-license/v1"
	// accountTrial stores the JSON trialRecord (start + high-water lastSeen).
	accountTrial = "auk-trial/v1"
	// accountFingerprint stores the generated-once fallback machine id used
	// only when a hardware-derived id is unavailable.
	accountFingerprint = "auk-fingerprint/v1"
)

// ErrSecretNotFound is what keyringKeychain.Get returns for an absent entry,
// so callers can distinguish "nothing stored yet" (expected: first run) from
// a real keychain failure without matching on go-keyring's error directly.
var ErrSecretNotFound = errors.New("license: secret not found")

// keyringKeychain is the production Keychain backed by the OS keychain
// (macOS Keychain), the same vault environment secrets already use.
type keyringKeychain struct{}

// NewKeyringKeychain returns the production OS-keychain-backed Keychain.
func NewKeyringKeychain() Keychain { return keyringKeychain{} }

func (keyringKeychain) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("license: keyring get %s/%s: %w", service, account, err)
	}
	return v, nil
}

func (keyringKeychain) Set(service, account, value string) error {
	if err := keyring.Set(service, account, value); err != nil {
		return fmt.Errorf("license: keyring set %s/%s: %w", service, account, err)
	}
	return nil
}

func (keyringKeychain) Delete(service, account string) error {
	if err := keyring.Delete(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("license: keyring delete %s/%s: %w", service, account, err)
	}
	return nil
}
