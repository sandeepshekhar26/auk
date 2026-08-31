// Command mkkeypair generates a fresh Ed25519 licence-signing keypair.
//
// This exists for exactly one operation: rotating the key AUK verifies
// licences against. It is run by hand, rarely, and its output is handled with
// care — the private half it writes is the only thing standing between the
// product and anyone minting licences the app accepts.
//
// Usage:
//
//	go run ./cmd/mkkeypair -out ~/auk-prod-license-key.b64
//
// It prints the PUBLIC key (paste into internal/license/keys.go) to stdout and
// writes the PRIVATE key, base64, mode 0600, to -out. The private key must
// then be moved into the signing worker's secret store and DELETED from disk:
//
//	wrangler secret put AUK_LICENSE_PRIVATE_KEY   # paste the file's contents
//	rm -P ~/auk-prod-license-key.b64
//
// The tool refuses to overwrite an existing -out file, so a second accidental
// run cannot destroy a key that is already in production.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mkkeypair:", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "path to write the base64 private key (required)")
	flag.Parse()

	if *out == "" {
		return errors.New("-out is required (path for the private key)")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}

	// O_EXCL: never clobber a key that may already be live. 0600 so the file
	// is unreadable by other accounts on this machine for the short window it
	// exists at all.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(0o600))
	if err != nil {
		return fmt.Errorf("create %s (refusing to overwrite an existing key): %w", *out, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv)); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	fmt.Printf("public key (paste into internal/license/keys.go):\n%s\n\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("private key written to %s (mode 0600)\n", *out)
	fmt.Println("Move it into the signing worker's secrets, then delete the file.")
	return nil
}
