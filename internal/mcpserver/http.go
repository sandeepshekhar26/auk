package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewToken returns a fresh 32-byte hex bearer token for the HTTP transport.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HTTPServer is a running embedded Streamable-HTTP MCP endpoint.
type HTTPServer struct {
	URL   string // e.g. http://127.0.0.1:52301/mcp
	Token string
	srv   *http.Server
}

// StartHTTP serves the MCP server over Streamable HTTP on 127.0.0.1 with
// bearer-token auth. port 0 picks a free port. Loopback + token is
// necessary-but-not-sufficient (any local process could try); the real gate
// for mutating actions is the engine's approval policy — this transport just
// keeps casual local processes and browser-origin requests out.
func StartHTTP(server *mcp.Server, port int, token string) (*HTTPServer, error) {
	if token == "" {
		return nil, errors.New("bearer token required")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(token, handler))

	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		// ErrServerClosed is the normal shutdown path; anything else would
		// have surfaced at Listen time in practice.
		_ = hs.Serve(ln)
	}()

	return &HTTPServer{
		URL:   fmt.Sprintf("http://%s/mcp", ln.Addr().String()),
		Token: token,
		srv:   hs,
	}, nil
}

// Stop shuts the endpoint down, closing active sessions.
func (h *HTTPServer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.srv.Shutdown(ctx)
}

func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		got, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="apitool-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
