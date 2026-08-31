package license

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSigningServer stands in for the licence-signing worker: it signs whatever
// it is asked to, with a key the test also holds, so the whole activation path
// (request shape, response parsing, verification, storage) runs for real.
func newSigningServer(t *testing.T, priv ed25519.PrivateKey) (*httptest.Server, *[]activationRequest) {
	t.Helper()
	var seen []activationRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/licenses/activate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req activationRequest
		_ = json.Unmarshal(body, &req)
		seen = append(seen, req)

		lic := License{
			LicenseKey:       req.LicenseKey,
			Email:            "buyer@example.com",
			Name:             "A Buyer",
			Plan:             "personal",
			MachineID:        req.Fingerprint,
			MaxMachines:      3,
			MachineCount:     1,
			IssuedAt:         time.Now().UTC().Truncate(time.Second),
			ExpiresUpdatesAt: time.Now().UTC().Truncate(time.Second).Add(365 * 24 * time.Hour),
		}
		signed, err := Sign(priv, lic)
		if err != nil {
			t.Errorf("sign: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signed)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRemoteActivatorReturnsAVerifiableLicense(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv, seen := newSigningServer(t, priv)

	act := remoteActivator{baseURL: srv.URL}
	signed, err := act.Activate(context.Background(), "AUK-4KDR2-8QW1M-VZ0PT-N7C93", "hw-this-machine")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := verifyWith(pub, signed); err != nil {
		t.Fatalf("returned licence did not verify: %v", err)
	}
	if signed.License.MachineID != "hw-this-machine" {
		t.Fatalf("machineId = %q, want the fingerprint we sent", signed.License.MachineID)
	}
	// The wire contract with the worker: these exact JSON field names.
	if len(*seen) != 1 || (*seen)[0].LicenseKey != "AUK-4KDR2-8QW1M-VZ0PT-N7C93" ||
		(*seen)[0].Fingerprint != "hw-this-machine" {
		t.Fatalf("worker received %+v", *seen)
	}
}

// The end-to-end path a real buyer takes: paste key → Manager activates
// remotely → licence is verified against the embedded key and stored → Status
// reports licensed, offline, from then on.
func TestManagerActivatesThroughTheRemoteWorker(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newSigningServer(t, priv)

	kc := newFakeKeychain()
	dir := t.TempDir()
	m := newManagerWith(dir, kc, NewFingerprinter(kc), remoteActivator{baseURL: srv.URL}, pub, time.Now)

	st, err := m.Activate(context.Background(), "AUK-4KDR2-8QW1M-VZ0PT-N7C93")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if st.State != StateLicensed {
		t.Fatalf("state = %q (%s), want licensed", st.State, st.Message)
	}
	// Offline from here: a manager with no server at all still reports licensed.
	offline := newManagerWith(dir, kc, NewFingerprinter(kc), remoteActivator{baseURL: "http://127.0.0.1:1"}, pub, time.Now)
	if got := offline.Status(); got.State != StateLicensed {
		t.Fatalf("offline state = %q, want licensed", got.State)
	}
}

func TestRemoteActivatorSurfacesTheWorkersOwnMessage(t *testing.T) {
	// The worker writes messages meant for the customer; AUK must not replace
	// them with something vaguer.
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"seat limit", http.StatusConflict,
			`{"error":"seat_limit","message":"This licence is already active on 3 of 3 Macs."}`,
			"already active on 3 of 3 Macs"},
		{"revoked", http.StatusForbidden,
			`{"error":"revoked","message":"This licence was refunded and can no longer be activated."}`,
			"refunded"},
		{"unknown key", http.StatusNotFound,
			`{"error":"unknown_key","message":"We don't recognise that licence key."}`,
			"recognise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := remoteActivator{baseURL: srv.URL}.Activate(context.Background(), "AUK-K", "hw-1")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestRemoteActivatorFallsBackWhenTheBodyIsNotOurs(t *testing.T) {
	// A Cloudflare error page or captive portal stands in for the worker: the
	// status code is all we have, and the user still needs a real sentence.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<html>404 not found</html>")
	}))
	defer srv.Close()

	_, err := remoteActivator{baseURL: srv.URL}.Activate(context.Background(), "AUK-K", "hw-1")
	if err == nil || !strings.Contains(err.Error(), "recognise that licence key") {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoteActivatorDistinguishesUnreachableFromRefused(t *testing.T) {
	// Port 1 on loopback refuses instantly — a stand-in for being offline.
	_, err := remoteActivator{baseURL: "http://127.0.0.1:1"}.Activate(context.Background(), "AUK-K", "hw-1")
	if !errors.Is(err, ErrRemoteActivationUnreachable) {
		t.Fatalf("error = %v, want ErrRemoteActivationUnreachable", err)
	}

	// A 5xx is also "couldn't ask", not "no" — the user should retry, not
	// conclude their licence is bad.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err = remoteActivator{baseURL: srv.URL}.Activate(context.Background(), "AUK-K", "hw-1")
	if !errors.Is(err, ErrRemoteActivationUnreachable) {
		t.Fatalf("5xx error = %v, want ErrRemoteActivationUnreachable", err)
	}
}

func TestRemoteActivatorRejectsAnUnsignedResponse(t *testing.T) {
	// A 200 with a licence but no signature must never be stored. Without this
	// check a hostile endpoint could hand out unsigned "licences" and the only
	// thing standing in the way would be the Manager's later verification.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"license":{"licenseKey":"AUK-K","email":"a@b.c"},"alg":"ed25519"}`)
	}))
	defer srv.Close()

	_, err := remoteActivator{baseURL: srv.URL}.Activate(context.Background(), "AUK-K", "hw-1")
	if err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("error = %v, want an 'unsigned licence' rejection", err)
	}
}

func TestRemoteActivatorDoesNotReadAnUnboundedBody(t *testing.T) {
	// A hostile or broken endpoint must not be able to stream us out of memory.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("A", 64<<10)
		for i := 0; i < 32; i++ { // 2 MiB, far past the cap
			_, _ = io.WriteString(w, chunk)
		}
	}))
	defer srv.Close()

	_, err := remoteActivator{baseURL: srv.URL}.Activate(context.Background(), "AUK-K", "hw-1")
	if err == nil {
		t.Fatal("expected an error for a garbage response")
	}
}

func TestRemoteActivatorValidatesItsInputs(t *testing.T) {
	act := remoteActivator{baseURL: "http://127.0.0.1:1"}
	if _, err := act.Activate(context.Background(), "   ", "hw-1"); err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if _, err := act.Activate(context.Background(), "AUK-K", " "); err == nil {
		t.Fatal("expected an error for an empty fingerprint")
	}
}

func TestEndpointDefaultsToTheProductionWorker(t *testing.T) {
	if got := (remoteActivator{}).endpoint(); got != DefaultActivationBaseURL+"/v1/licenses/activate" {
		t.Fatalf("endpoint = %q", got)
	}
	if !strings.HasPrefix(DefaultActivationBaseURL, "https://") {
		t.Fatalf("the production activation endpoint must be HTTPS, got %q", DefaultActivationBaseURL)
	}
	// A trailing slash in configuration must not produce a double slash.
	if got := (remoteActivator{baseURL: "https://x.test/api/"}).endpoint(); got != "https://x.test/api/v1/licenses/activate" {
		t.Fatalf("endpoint = %q", got)
	}
}

// Deactivation must clear locally even when the seat-release call fails —
// otherwise an offline user can never move their licence to another Mac.
func TestDeactivateClearsLocallyEvenWhenTheWorkerIsUnreachable(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newSigningServer(t, priv)

	kc := newFakeKeychain()
	dir := t.TempDir()
	m := newManagerWith(dir, kc, NewFingerprinter(kc), remoteActivator{baseURL: srv.URL}, pub, time.Now)
	if _, err := m.Activate(context.Background(), "AUK-K"); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Point the manager at a dead endpoint, then deactivate.
	m.activator = remoteActivator{baseURL: "http://127.0.0.1:1"}
	if err := m.Deactivate(); err != nil {
		t.Fatalf("deactivate must succeed locally even offline, got: %v", err)
	}
	if st := m.Status(); st.State == StateLicensed {
		t.Fatal("licence was still stored after deactivation")
	}
}

// The happy path for seat release: the worker is told which machine to free.
func TestDeactivateReleasesTheSeatServerSide(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var released []activationRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/licenses/activate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req activationRequest
		_ = json.Unmarshal(body, &req)
		signed, _ := Sign(priv, License{
			LicenseKey: req.LicenseKey, Email: "b@e.com", Plan: "personal",
			MachineID: req.Fingerprint, MaxMachines: 3, MachineCount: 1,
			IssuedAt:         time.Now().UTC().Truncate(time.Second),
			ExpiresUpdatesAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour),
		})
		_ = json.NewEncoder(w).Encode(signed)
	})
	mux.HandleFunc("/v1/licenses/deactivate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req activationRequest
		_ = json.Unmarshal(body, &req)
		released = append(released, req)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	kc := newFakeKeychain()
	m := newManagerWith(t.TempDir(), kc, NewFingerprinter(kc), remoteActivator{baseURL: srv.URL}, pub, time.Now)
	if _, err := m.Activate(context.Background(), "AUK-SEAT-TEST"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := m.Deactivate(); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if len(released) != 1 || released[0].LicenseKey != "AUK-SEAT-TEST" || released[0].Fingerprint == "" {
		t.Fatalf("worker saw releases %+v, want one naming this machine", released)
	}
}
