package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"apitool/internal/appcore"
)

func startTestHTTP(t *testing.T) *HTTPServer {
	t.Helper()
	engine, store, err := appcore.NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	hs, err := StartHTTP(New(engine, store), 0, token)
	if err != nil {
		t.Fatalf("StartHTTP: %v", err)
	}
	t.Cleanup(hs.Stop)
	return hs
}

func TestHTTPRejectsWithoutToken(t *testing.T) {
	hs := startTestHTTP(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for name, headers := range map[string]map[string]string{
		"no auth header": {},
		"wrong token":    {"Authorization": "Bearer wrong"},
	} {
		req, _ := http.NewRequest("POST", hs.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
	}
}

func TestHTTPInitializeWithToken(t *testing.T) {
	hs := startTestHTTP(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest("POST", hs.URL, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+hs.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	raw, _ := io.ReadAll(resp.Body)
	// Streamable HTTP may answer as SSE; extract the data line if so.
	payload := string(raw)
	if i := strings.Index(payload, "data: "); i >= 0 {
		payload = payload[i+6:]
		if j := strings.IndexByte(payload, '\n'); j > 0 {
			payload = payload[:j]
		}
	}
	var msg struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("parse initialize response %q: %v", payload, err)
	}
	if msg.Result.ServerInfo.Name != "apitool" {
		t.Errorf("serverInfo.name = %q, want apitool", msg.Result.ServerInfo.Name)
	}
}
