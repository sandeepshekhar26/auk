// Command mklicense mints a signed AUK license for testing.
//
// It reads the DEV Ed25519 private key (base64) and signs a license.License
// with it, printing the resulting SignedLicense as JSON. Because the app's
// embedded PUBLIC key is the matching half of this dev key, a license minted
// here can be pasted straight into AUK's "Activate" box to activate offline —
// which is how the licensing feature is tested end-to-end without a Merchant
// of Record yet.
//
// Usage:
//
//	go run ./cmd/mklicense -email you@example.com -name "You" [flags]
//
// Flags:
//
//	-email    (required) buyer email
//	-name     buyer name
//	-plan     plan label (default "personal")
//	-days     updates-window length in days from now (default 365 = 12 months)
//	-machine  fingerprint to bind the license to. Default: THIS machine's
//	          fingerprint, so the minted license activates on this Mac. Pass a
//	          fixed value to mint for another machine or a test vector.
//	-key      opaque license key to embed (default: a random test key)
//	-privkey  path to the base64 dev private key (default: the session
//	          scratchpad key this build's public key was generated with)
//	-base64   emit base64(JSON) instead of indented JSON (a single-line blob)
//	-out      write to this file instead of stdout
//
// PRODUCTION NOTE: this same signing step is what the license-issuing worker
// (behind the Lemon Squeezy / Paddle webhook) will do — but with the
// PRODUCTION private key, which must never live on a dev machine or in this
// repo. See docs/06-licensing.md.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"apitool/internal/license"
)

// defaultDevKeyPath is the session scratchpad file the dev keypair was written
// to when this build's embedded public key was generated. It is intentionally
// outside the repo and uncommitted.
const defaultDevKeyPath = "/private/tmp/claude-501/-Users-skumar-repos-api-tool/02601c84-3093-4579-a380-e66cf23a9f7f/scratchpad/auk_license_ed25519.key"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mklicense:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		email    = flag.String("email", "", "buyer email (required)")
		name     = flag.String("name", "", "buyer name")
		plan     = flag.String("plan", "personal", "plan label")
		days     = flag.Int("days", 365, "updates-window length in days from now")
		machine  = flag.String("machine", "", "machine fingerprint to bind to (default: this machine)")
		key      = flag.String("key", "", "opaque license key to embed (default: random test key)")
		privPath = flag.String("privkey", defaultDevKeyPath, "path to base64 dev private key")
		asBase64 = flag.Bool("base64", false, "emit base64(JSON) instead of indented JSON")
		out      = flag.String("out", "", "write to this file instead of stdout")
	)
	flag.Parse()

	if strings.TrimSpace(*email) == "" {
		return fmt.Errorf("-email is required")
	}

	priv, err := loadPrivateKey(*privPath)
	if err != nil {
		return err
	}

	fingerprint := strings.TrimSpace(*machine)
	if fingerprint == "" {
		fingerprint, err = license.NewFingerprinter(license.NewKeyringKeychain()).MachineID()
		if err != nil {
			return fmt.Errorf("resolve this machine's fingerprint (pass -machine to override): %w", err)
		}
	}

	licenseKey := strings.TrimSpace(*key)
	if licenseKey == "" {
		licenseKey = randomKey()
	}

	now := time.Now().UTC()
	lic := license.License{
		LicenseKey:       licenseKey,
		Email:            *email,
		Name:             *name,
		Plan:             *plan,
		MachineID:        fingerprint,
		MaxMachines:      license.DefaultMaxMachines,
		MachineCount:     1,
		IssuedAt:         now,
		ExpiresUpdatesAt: now.Add(time.Duration(*days) * 24 * time.Hour),
	}

	signed, err := license.Sign(priv, lic)
	if err != nil {
		return err
	}

	blob, err := render(signed, *asBase64)
	if err != nil {
		return err
	}

	if *out != "" {
		if err := os.WriteFile(*out, []byte(blob+"\n"), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", *out, err)
		}
		fmt.Fprintf(os.Stderr, "wrote signed license for %s (bound to %s) to %s\n", *email, fingerprint, *out)
		return nil
	}
	fmt.Println(blob)
	return nil
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dev private key at %s (generate one and set -privkey, see docs/06-licensing.md): %w", path, err)
	}
	priv, err := license.ParsePrivateKeyBase64(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	return priv, nil
}

func render(signed license.SignedLicense, asBase64 bool) (string, error) {
	if asBase64 {
		compact, err := json.Marshal(signed)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(compact), nil
	}
	indented, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return "", err
	}
	return string(indented), nil
}

func randomKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "TEST-KEY-FALLBACK"
	}
	// Group into a readable AUK-XXXX-XXXX-XXXX-XXXX shape.
	hexStr := strings.ToUpper(fmt.Sprintf("%x", b))
	return "AUK-" + hexStr[0:4] + "-" + hexStr[4:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16]
}
