package license

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// licenseFileName mirrors the stored license to the app data dir (~/.auk).
// The keychain is the primary store; this file is a resilience copy so a
// keychain reset (which a paying customer might trigger by accident) does not
// silently drop a valid, paid-for license — it can still be read back from
// here and re-verified offline. A leading dot keeps it inconspicuous.
const licenseFileName = ".license"

// storedLicense wraps the signed license with local activation bookkeeping.
// The Signed portion is what carries trust (it's signature-verified on every
// read); ActivatedAt/LastValidatedAt are local metadata only and are NOT part
// of the signed payload, so they are never trusted for anything security-
// relevant — LastValidatedAt only drives the soft "recently re-checked
// online" grace flag.
type storedLicense struct {
	Signed      SignedLicense `json:"signed"`
	ActivatedAt time.Time     `json:"activatedAt"`
	// LastValidatedAt is the last time an online re-check with the issuer
	// succeeded. Set to activation time initially; a future periodic re-check
	// (see remoteActivator) updates it. Drives grace only — never validity.
	LastValidatedAt time.Time `json:"lastValidatedAt"`
}

// licenseStore reads/writes the stored license across keychain (primary) and
// file mirror (fallback).
type licenseStore struct {
	kc      Keychain
	dataDir string
}

func (s *licenseStore) licenseFilePath() string {
	return filepath.Join(s.dataDir, licenseFileName)
}

// load returns the stored license, preferring the keychain and falling back
// to the file mirror. The bool is false when nothing is stored in either
// place (the normal trial-user state). No signature checking happens here —
// that is the caller's job on every use (status.go), so a tampered mirror
// file is caught at verification, not trusted because it loaded.
func (s *licenseStore) load() (storedLicense, bool) {
	if v, err := s.kc.Get(keychainService, accountLicense); err == nil && v != "" {
		var sl storedLicense
		if json.Unmarshal([]byte(v), &sl) == nil {
			return sl, true
		}
	}
	if b, err := os.ReadFile(s.licenseFilePath()); err == nil {
		var sl storedLicense
		if json.Unmarshal(b, &sl) == nil {
			// Self-heal: repopulate the keychain from the surviving mirror so
			// the primary store is whole again next time.
			if b2, mErr := json.Marshal(sl); mErr == nil {
				_ = s.kc.Set(keychainService, accountLicense, string(b2))
			}
			return sl, true
		}
	}
	return storedLicense{}, false
}

// save writes the stored license to both the keychain and the file mirror.
// The keychain write is the one that must succeed; the mirror is best-effort.
func (s *licenseStore) save(sl storedLicense) error {
	b, err := json.Marshal(sl)
	if err != nil {
		return err
	}
	if err := s.kc.Set(keychainService, accountLicense, string(b)); err != nil {
		return err
	}
	if mkErr := os.MkdirAll(s.dataDir, 0o755); mkErr == nil {
		tmp := s.licenseFilePath() + ".tmp"
		if wErr := os.WriteFile(tmp, b, 0o600); wErr == nil {
			_ = os.Rename(tmp, s.licenseFilePath())
		}
	}
	return nil
}

// clear removes the stored license from both stores (deactivation). Missing
// entries are not an error — deactivating when nothing is stored is a no-op.
func (s *licenseStore) clear() error {
	if err := s.kc.Delete(keychainService, accountLicense); err != nil {
		return err
	}
	if err := os.Remove(s.licenseFilePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
