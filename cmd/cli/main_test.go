package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/storage"
)

func TestExitError(t *testing.T) {
	tests := []struct {
		name    string
		resp    model.ResponseData
		runErr  error
		wantErr bool
	}{
		{name: "success 200", resp: model.ResponseData{Status: 200}, wantErr: false},
		{name: "success 399", resp: model.ResponseData{Status: 399}, wantErr: false},
		{name: "client error 400", resp: model.ResponseData{Status: 400}, wantErr: true},
		{name: "server error 500", resp: model.ResponseData{Status: 500}, wantErr: true},
		{name: "engine error takes priority", resp: model.ResponseData{Status: 200}, runErr: errors.New("boom"), wantErr: true},
		{name: "zero-value response is not an error by itself", resp: model.ResponseData{}, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exitError("req-1", tt.resp, tt.runErr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("exitError() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRunRequestThroughCLIEngine(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantErr    bool
	}{
		{
			name: "2xx is success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"ok":true}`))
			},
			wantStatus: 200,
			wantErr:    false,
		},
		{
			name: "5xx is a CLI error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantStatus: 500,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			store, err := storage.NewFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}
			requestID, err := seedDemoData(store, srv.URL)
			if err != nil {
				t.Fatalf("seedDemoData() error = %v", err)
			}
			engine := buildEngine(store)

			resp, runErr := engine.RunRequest(context.Background(), uuid.NewString(), requestID, "", "cli", core.NoopSink{})
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (runErr=%v)", resp.Status, tt.wantStatus, runErr)
			}

			err = exitError(requestID, resp, runErr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("exitError() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewStoreSeedsARunnableRequest(t *testing.T) {
	store, err := newStore(t.TempDir())
	if err != nil {
		t.Fatalf("newStore() error = %v", err)
	}
	engine := buildEngine(store)

	requests := store.(*storage.FileStore).ListRequests("")
	if len(requests) != 1 {
		t.Fatalf("expected exactly one seeded request, got %d", len(requests))
	}

	if _, err := engine.Store.GetRequest(requests[0].ID); err != nil {
		t.Fatalf("seeded request not resolvable through the engine's store: %v", err)
	}
}

// TestReorderFlagsFirst guards against a real bug caught by manual
// end-to-end testing: flag.FlagSet.Parse stops consuming flags at the
// first positional token, so `run <requestID> --workspace-dir=X` silently
// discarded the flag and fell back to the working directory — pointing the
// CLI at an empty/wrong workspace instead of erroring loudly.
func TestReorderFlagsFirst(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags already first (baseline)",
			args: []string{"--workspace-dir=X", "req-1"},
			want: []string{"--workspace-dir=X", "req-1"},
		},
		{
			name: "positional before inline-value flag",
			args: []string{"req-1", "--workspace-dir=X"},
			want: []string{"--workspace-dir=X", "req-1"},
		},
		{
			name: "positional before space-separated flag value",
			args: []string{"req-1", "--workspace-dir", "X"},
			want: []string{"--workspace-dir", "X", "req-1"},
		},
		{
			name: "positional between two flags",
			args: []string{"--workspace-dir=X", "req-1", "--env=Y"},
			want: []string{"--workspace-dir=X", "--env=Y", "req-1"},
		},
		{
			name: "unrecognized flag stays positional (passed through to flag.Parse to error)",
			args: []string{"req-1", "--bogus=Z"},
			want: []string{"req-1", "--bogus=Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderFlagsFirst(tt.args, "workspace-dir", "env")
			if len(got) != len(tt.want) {
				t.Fatalf("reorderFlagsFirst(%v) = %v, want %v", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("reorderFlagsFirst(%v) = %v, want %v", tt.args, got, tt.want)
				}
			}
		})
	}
}

// TestRunFlagOrderIndependence is the regression test for the actual bug:
// the same request must be found whether --workspace-dir comes before or
// after the positional requestID.
func TestRunFlagOrderIndependence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	requestID, err := seedDemoData(store, srv.URL)
	if err != nil {
		t.Fatalf("seedDemoData() error = %v", err)
	}

	flagFirst := []string{"run", "--workspace-dir=" + dir, requestID}
	positionalFirst := []string{"run", requestID, "--workspace-dir=" + dir}

	if err := run(flagFirst); err != nil {
		t.Fatalf("run(%v) error = %v, want nil", flagFirst, err)
	}
	if err := run(positionalFirst); err != nil {
		t.Fatalf("run(%v) error = %v, want nil (this is the exact bug: flag silently ignored, CLI falls back to cwd and can't find the request)", positionalFirst, err)
	}
}

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no args", args: nil},
		{name: "unknown subcommand", args: []string{"send"}},
		{name: "run with no request id", args: []string{"run"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(tt.args); err == nil {
				t.Fatal("expected a usage error, got nil")
			}
		})
	}
}
