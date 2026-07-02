package http

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// generateSelfSignedCert returns a tls.Certificate suitable for
// tls.Config.Certificates plus its PEM-encoded certificate and key, so
// tests can exercise BuildTLSConfig with real PEM bytes rather than
// hand-rolled fixtures.
func generateSelfSignedCert(t *testing.T, cn string) (tls.Certificate, []byte, []byte) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM := pemEncode(t, "CERTIFICATE", derBytes)

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ec private key: %v", err)
	}
	keyPEM := pemEncode(t, "EC PRIVATE KEY", keyBytes)

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}

	return cert, certPEM, keyPEM
}

func certToPEM(t *testing.T, cert tls.Certificate) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	for _, der := range cert.Certificate {
		if err := pem.Encode(buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			t.Fatalf("pem encode: %v", err)
		}
	}
	return buf.Bytes()
}

func pemEncode(t *testing.T, blockType string, der []byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := pem.Encode(buf, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("pem encode: %v", err)
	}
	return buf.Bytes()
}
