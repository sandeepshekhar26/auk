package storage

import "fmt"

// fakeSecretStore is an in-memory SecretStore used by tests so they never
// touch the real OS keychain (go-keyring can pop a permission dialog on
// first use, which would hang a non-interactive test run).
type fakeSecretStore struct {
	values map[string]string
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{values: make(map[string]string)}
}

func (f *fakeSecretStore) key(service, account string) string {
	return service + "\x00" + account
}

func (f *fakeSecretStore) Get(service, account string) (string, error) {
	v, ok := f.values[f.key(service, account)]
	if !ok {
		return "", fmt.Errorf("secret not found: %s/%s", service, account)
	}
	return v, nil
}

func (f *fakeSecretStore) Set(service, account, value string) error {
	f.values[f.key(service, account)] = value
	return nil
}

func (f *fakeSecretStore) Delete(service, account string) error {
	delete(f.values, f.key(service, account))
	return nil
}
