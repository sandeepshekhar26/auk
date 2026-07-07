package storage

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// SecretStore is the minimal OS-credential-vault contract FileStore depends
// on for model.Environment.Secrets values. Kept as an interface (rather than
// calling go-keyring directly) so tests can swap in an in-memory fake —
// go-keyring hits the real OS keychain, which can pop a permission dialog on
// first use and must never run inside a non-interactive test.
type SecretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	// Delete removes a secret. Deleting one that was never set (e.g. a
	// variable marked secret but never given a value) is a no-op, not an
	// error — mirrors FileStore's own "removing something already gone"
	// convention (see RemoveMcpConnection).
	Delete(service, account string) error
}

// KeyringSecretStore is the production SecretStore backed by the OS
// keychain (macOS Keychain / Windows Credential Manager / Linux Secret
// Service via go-keyring).
type KeyringSecretStore struct{}

func NewKeyringSecretStore() KeyringSecretStore { return KeyringSecretStore{} }

func (KeyringSecretStore) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if err != nil {
		return "", fmt.Errorf("keyring get %s/%s: %w", service, account, err)
	}
	return v, nil
}

func (KeyringSecretStore) Set(service, account, value string) error {
	if err := keyring.Set(service, account, value); err != nil {
		return fmt.Errorf("keyring set %s/%s: %w", service, account, err)
	}
	return nil
}

func (KeyringSecretStore) Delete(service, account string) error {
	if err := keyring.Delete(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("keyring delete %s/%s: %w", service, account, err)
	}
	return nil
}

const secretServiceName = "apitool"

// secretAccount builds the go-keyring "account" for one environment
// variable's secret value, namespaced by workspace + environment so the
// same variable name in two environments never collides in the keychain.
func secretAccount(workspaceID, envID, varName string) string {
	return workspaceID + "/" + envID + "/" + varName
}
