package license

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// prodSignedLicenseB64 is a real SignedLicense minted by cmd/mklicense with the
// DEV private key (the one whose public half is embedded as devPublicKeyBase64
// in keys.go). It is a frozen test vector: it proves the SHIPPED verification
// path — package-level Verify, using the embedded public key — accepts a
// license produced by the actual signing tool, end to end, with no ephemeral
// keys involved. Regenerate with:
//
//	go run ./cmd/mklicense -email vector@auk.test -name "Vector Test" \
//	  -plan personal -days 365 -machine hw-fixturemachine00000000000000 \
//	  -key AUK-TEST-VECTOR-0001 -base64 -privkey <path to the private key>
//
// Regenerating requires temporary access to the production private key, which
// normally lives only in the signing worker's secrets — so in practice this is
// re-minted only during a key rotation, from the key file before it is deleted.
const prodSignedLicenseB64 = "eyJsaWNlbnNlIjp7ImxpY2Vuc2VLZXkiOiJBVUstVEVTVC1WRUNUT1ItMDAwMSIsImVtYWlsIjoidmVjdG9yQGF1ay50ZXN0IiwibmFtZSI6IlZlY3RvciBUZXN0IiwicGxhbiI6InBlcnNvbmFsIiwibWFjaGluZUlkIjoiaHctZml4dHVyZW1hY2hpbmUwMDAwMDAwMDAwMDAwMCIsIm1heE1hY2hpbmVzIjozLCJtYWNoaW5lQ291bnQiOjEsImlzc3VlZEF0IjoiMjAyNi0wOC0zMVQxMToxMjo0MS44NDYzNTVaIiwiZXhwaXJlc1VwZGF0ZXNBdCI6IjIwMjctMDgtMzFUMTE6MTI6NDEuODQ2MzU1WiJ9LCJhbGciOiJlZDI1NTE5Iiwic2lnbmF0dXJlIjoiYkI5bkpuRUdlRVh3ZDQzSDVRbEsyZURMU1pEYXhZZjRFWVpYYm1NelQ1d0liQ0Q3Zy85aWVLTE1UM09uMGx2bEhxeDJDby8xd3lKU245c2M2am9zRGc9PSJ9"

func decodeVector(t *testing.T) SignedLicense {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(prodSignedLicenseB64)
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
// public key accepts a genuinely production-key-signed license. If this fails,
// either the embedded key and the signing key have diverged, or canonicalBytes
// changed without re-minting the vector.
func TestEmbeddedKeyVerifiesRealVector(t *testing.T) {
	if err := Verify(decodeVector(t)); err != nil {
		t.Fatalf("embedded key rejected a real production-signed license: %v", err)
	}
}

// TestEmbeddedKeyRejectsTamperedVector proves the same shipped path rejects a
// one-field edit of that real license.
func TestEmbeddedKeyRejectsTamperedVector(t *testing.T) {
	sl := decodeVector(t)
	sl.License.Email = "attacker@evil.com" // any change invalidates the signature
	if err := Verify(sl); err == nil {
		t.Fatal("embedded key accepted a tampered production-signed license")
	}
}

// retiredDevSignedLicenseB64 is the vector that the RETIRED dev key signed —
// the key whose private half sat in a developer home directory before the
// production rotation. It is kept verbatim so this build can prove, on every
// test run, that the rotation actually took effect in the shipped verification
// path rather than only in a comment.
const retiredDevSignedLicenseB64 = "eyJsaWNlbnNlIjp7ImxpY2Vuc2VLZXkiOiJBVUstVEVTVC1WRUNUT1ItMDAwMSIsImVtYWlsIjoidmVjdG9yQGF1ay50ZXN0IiwibmFtZSI6IlZlY3RvciBUZXN0IiwicGxhbiI6InBlcnNvbmFsIiwibWFjaGluZUlkIjoiaHctZml4dHVyZW1hY2hpbmUwMDAwMDAwMDAwMDAwMCIsIm1heE1hY2hpbmVzIjozLCJtYWNoaW5lQ291bnQiOjEsImlzc3VlZEF0IjoiMjAyNi0wOC0zMFQwNjozMDozNC43NzE3NzFaIiwiZXhwaXJlc1VwZGF0ZXNBdCI6IjIwMjctMDgtMzBUMDY6MzA6MzQuNzcxNzcxWiJ9LCJhbGciOiJlZDI1NTE5Iiwic2lnbmF0dXJlIjoiMlFFOE9YbE4vczEwd3phMnkxajgwVTQvV0xrS1dWMWdhemljRnF0dHdBWmZLbllqcFBXeVlpdEtXMk5zbXN0MUE2Yi9qb1BaNE4wN3l0NnJsbmhIQlE9PSJ9"

// TestRetiredDevKeyIsNoLongerTrusted is the rotation guard. A license that the
// old dev key signed perfectly must now fail exactly like a forgery — because
// to this build it IS one. If this test ever passes a license again, the
// embedded key has been reverted to the compromised dev key.
func TestRetiredDevKeyIsNoLongerTrusted(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(retiredDevSignedLicenseB64)
	if err != nil {
		t.Fatalf("decode retired vector: %v", err)
	}
	var sl SignedLicense
	if err := json.Unmarshal(raw, &sl); err != nil {
		t.Fatalf("unmarshal retired vector: %v", err)
	}
	if err := Verify(sl); err == nil {
		t.Fatal("SECURITY: the retired dev signing key is still trusted by this build")
	}
}
