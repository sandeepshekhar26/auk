// Package appcore builds the one core.Engine that every entrypoint shares —
// the GUI (app.go), the CLI runner (cmd/cli), and the MCP server (cmd/mcp).
// Centralizing construction here is what guarantees "run a request" behaves
// identically no matter who initiated it (docs/02-architecture.md §1). This
// package has ZERO Wails imports, so the CLI and MCP binaries link without
// pulling in the GUI toolkit.
package appcore

import (
	"os"
	"path/filepath"

	"apitool/internal/assert"
	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	graphqlprotocol "apitool/internal/protocols/graphql"
	grpcprotocol "apitool/internal/protocols/grpc"
	httpprotocol "apitool/internal/protocols/http"
	sseprotocol "apitool/internal/protocols/sse"
	wsprotocol "apitool/internal/protocols/ws"
	"apitool/internal/scripting"
	"apitool/internal/storage"
	"apitool/internal/templating"
)

// DefaultWorkspaceDir is ~/.auk/workspace — the single on-disk location
// the GUI reads/writes and the CLI/MCP consumers read. Falls back to a
// relative dir if the home directory can't be resolved.
func DefaultWorkspaceDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".auk", "workspace")
	}
	return "workspace"
}

// NewEngine opens the file-backed store at dir and wires the full engine:
// every protocol registered, the templater bound as its own chain resolver
// (for response('Name') auto-send refs), and the default allow-all policy.
// A caller that needs a stricter policy (e.g. MCP with production gates) can
// replace engine.Policy after construction.
func NewEngine(dir string) (*core.Engine, *storage.FileStore, error) {
	store, err := storage.NewFileStore(dir)
	if err != nil {
		return nil, nil, err
	}

	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.Asserter = asserter{}
	engine.Scripter = scripting.New()
	engine.RegisterProtocol(httpprotocol.New())
	engine.RegisterProtocol(wsprotocol.New())
	engine.RegisterProtocol(sseprotocol.New())
	engine.RegisterProtocol(graphqlprotocol.New())
	engine.RegisterProtocol(grpcprotocol.New())

	return engine, store, nil
}

// asserter adapts internal/assert to core.Asserter, keeping the assert
// package out of core's imports.
type asserter struct{}

func (asserter) Evaluate(assertions []model.Assertion, resp model.ResponseData) []model.AssertionResult {
	return assert.Evaluate(assertions, resp)
}
