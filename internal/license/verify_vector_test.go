package license

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// devSignedLicenseB64 is a real SignedLicense minted by cmd/mklicense with the
// DEV private key (the one whose public half is embedded as devPublicKeyBase64
// in keys.go). It is a frozen test vector: it proves the SHIPPED verification
// path — package-level Verify, using the embedded public key — accepts a
// license produced by the actual signing tool, end to end, with no ephemeral
// keys involved. Regenerate with:
//
//	go run ./cmd/mklicense -email vector@auk.test -name "Vector Test" \
//	  -plan personal -days 365 -machine hw-fixturemachine00000000000000 \
//	  -key AUK-TEST-VECTOR-0001 -base64
//
// (only necessary if the embedded key ever changes — e.g. the production key
// swap, at which point this vector must be re-minted with the new key or
// deleted).
const devSignedLicenseB64 = "eyJsaWNlbnNlIjp7ImxpY2Vuc2VLZXkiOiJBVUstVEVTVC1WRUNUT1ItMDAwMSIsImVtYWlsIjoidmVjdG9yQGF1ay50ZXN0IiwibmFtZSI6IlZlY3RvciBUZXN0IiwicGxhbiI6InBlcnNvbmFsIiwibWFjaGluZUlkIjoiaHctZml4dHVyZW1hY2hpbmUwMDAwMDAwMDAwMDAwMCIsIm1heE1hY2hpbmVzIjozLCJtYWNoaW5lQ291bnQiOjEsImlzc3VlZEF0IjoiMjAyNi0wOC0zMFQwNjozMDozNC43NzE3NzFaIiwiZXhwaXJlc1VwZGF0ZXNBdCI6IjIwMjctMDgtMzBUMDY6MzA6MzQuNzcxNzcxWiJ9LCJhbGciOiJlZDI1NTE5Iiwic2lnbmF0dXJlIjoiMlFFOE9YbE4vczEwd3phMnkxajgwVTQvV0xrS1dWMWdhemljRnF0dHdBWmZLbllqcFBXeVlpdEtXMk5zbXN0MUE2Yi9qb1BaNE4wN3l0NnJsbmhIQlE9PSJ9"

func decodeVector(t *testing.T) SignedLicense {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(devSignedLicenseB64)
	if err != nil {
		t.Fatalf("decode vector: %v", err)
	}
	var sl SignedLicense
	if err := json.Unmarshal(raw, &sl); err != nil {
		t.Fatalf("unmarshal vector: %v", err)
	}
	return sl
}

// TestEmbeddedKeyVerifiesRealVector is the end-to-end proof that the embedded
// public key accepts a genuinely dev-key-signed license. If this fails, either
// the embedded key and the scratchpad dev key have diverged, or canonicalBytes
// changed without re-minting the vector.
func TestEmbeddedKeyVerifiesRealVector(t *testing.T) {
	if err := Verify(decodeVector(t)); err != nil {
		t.Fatalf("embedded key rejected a real dev-signed license: %v", err)
	}
}

// TestEmbeddedKeyRejectsTamperedVector proves the same shipped path rejects a
// one-field edit of that real license.
func TestEmbeddedKeyRejectsTamperedVector(t *testing.T) {
	sl := decodeVector(t)
	sl.License.Email = "attacker@evil.com" // any change invalidates the signature
	if err := Verify(sl); err == nil {
		t.Fatal("embedded key accepted a tampered dev-signed license")
	}
}
