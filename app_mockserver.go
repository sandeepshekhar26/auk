package main

import (
	"sync"

	"apitool/internal/mockserver"
	"apitool/internal/storage"
)

// ---------------------------------------------------------------------------
// Mock server bindings (Settings → Mock Server).
//
// Serves the selected workspace's RECORDED responses on loopback so a
// frontend can develop against an API that doesn't exist yet or is down.
// See docs/10-mock-server.md and internal/mockserver.
//
// State lives in package-level vars rather than on *App (which app.go owns)
// because the mock is a ONE-PER-PROCESS resource anyway: it binds a single
// fixed TCP port, and AUK runs exactly one App per process (main.go). Two
// concurrent mocks would fight over the port, so there is nothing a per-App
// field would buy. mockMu guards all three vars together; every exported
// method below takes it for its whole body, which also makes Start/Stop
// mutually exclusive rather than racing over the listener.
//
// Deliberately NOT auto-started at launch (unlike the MCP server, whose
// enabled-flag startup lives in app.go's startup()): a mock that silently
// binds a port every time the app opens is a surprise, and the workspace to
// serve isn't known until the user picks one. The chosen PORT is persisted;
// the running state is not.
// ---------------------------------------------------------------------------

var (
	mockMu    sync.Mutex
	mockSrv   *mockserver.Server
	mockError string
)

// MockStatus is what the Settings UI renders.
type MockStatus struct {
	Running     bool   `json:"running"`
	Port        int    `json:"port"`
	WorkspaceID string `json:"workspaceId"`
	// Routes is the number of mock endpoints currently served — read live
	// from the store, so it grows the moment another request is sent in the
	// app. MockServerRoutes returns the list itself.
	Routes int    `json:"routes"`
	Error  string `json:"error,omitempty"`
}

// MockRoute is one row of the route listing in Settings. A flat mirror of
// mockserver.Route (which carries unexported compiled-matcher fields) so the
// binding surface stays a plain data struct.
type MockRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	RequestID   string `json:"requestId"`
	RequestName string `json:"requestName"`
	Status      int    `json:"status"`
}

// StartMockServer starts (or restarts) the mock for workspaceID on port, and
// persists the port so the next launch offers the same one.
//
// port <= 0 means "use the remembered port, else the default" — the frontend
// sends 0 for an untouched input rather than having to know the default.
//
// Idempotent in the useful sense: starting with the same workspace+port while
// already running is a no-op that just reports status; starting with a
// DIFFERENT workspace or port stops the old listener and rebinds, so the
// button never lands the user in a state where the label says one thing and
// the port serves another.
func (a *App) StartMockServer(workspaceID string, port int) (MockStatus, error) {
	if port <= 0 {
		port = a.GetSettings().MockPort
	}
	if port <= 0 {
		port = mockserver.DefaultPort
	}

	mockMu.Lock()
	defer mockMu.Unlock()

	if mockSrv != nil {
		if mockSrv.WorkspaceID() == workspaceID && mockSrv.Port() == port {
			return statusLocked(), nil
		}
		mockSrv.Stop()
		mockSrv = nil
	}

	srv, err := mockserver.Start(a.store, workspaceID, port)
	if err != nil {
		mockError = err.Error()
		return statusLocked(), err
	}
	mockSrv = srv
	mockError = ""

	// Persist the port only once it's proven bindable — remembering a port
	// that failed would reoffer the same failure on the next launch.
	// Read-modify-write of the WHOLE settings object, same rule as
	// SetMCPEnabled: SaveSettings overwrites the file.
	s := a.GetSettings()
	if s.MockPort != srv.Port() {
		s.MockPort = srv.Port()
		_ = storage.SaveSettings(storage.DefaultSettingsPath(), s)
	}
	return statusLocked(), nil
}

// StopMockServer stops the mock. Stopping an already-stopped server is a
// no-op that reports the stopped status, not an error.
func (a *App) StopMockServer() MockStatus {
	mockMu.Lock()
	defer mockMu.Unlock()
	if mockSrv != nil {
		mockSrv.Stop()
		mockSrv = nil
	}
	mockError = ""
	return statusLocked()
}

// MockServerStatus reports the current state, including a LIVE route count
// (the store is re-read, so sending another request in the app raises this
// without restarting anything).
func (a *App) MockServerStatus() MockStatus {
	mockMu.Lock()
	defer mockMu.Unlock()
	return statusLocked()
}

// MockServerRoutes lists the endpoints the mock currently serves, in the
// stable order internal/mockserver sorts them into. Empty when stopped.
func (a *App) MockServerRoutes() []MockRoute {
	mockMu.Lock()
	defer mockMu.Unlock()
	out := []MockRoute{}
	if mockSrv == nil {
		return out
	}
	for _, r := range mockSrv.Routes() {
		out = append(out, MockRoute{
			Method:      r.Method,
			Path:        r.Path,
			RequestID:   r.RequestID,
			RequestName: r.RequestName,
			Status:      r.Status,
		})
	}
	return out
}

// statusLocked builds a MockStatus; callers must already hold mockMu.
func statusLocked() MockStatus {
	st := MockStatus{Error: mockError}
	if mockSrv != nil {
		st.Running = true
		st.Port = mockSrv.Port()
		st.WorkspaceID = mockSrv.WorkspaceID()
		st.Routes = len(mockSrv.Routes())
	}
	return st
}
