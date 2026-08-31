package mcpserver

import (
	"context"
	"fmt"
	"strings"
)

// Scope is the capability grant an MCP server is started with. It decides
// which tools are REGISTERED — not which calls are refused at runtime.
//
// That distinction is the whole design. A read-only server does not expose
// create_request and then say no; the tool is absent from tools/list, so the
// agent never plans around a capability it does not have, never burns a turn
// on a refusal, and never has its refusal mistaken for a transient error worth
// retrying. Least privilege is legible rather than enforced by apology.
type Scope string

const (
	// ScopeReadOnly: inspect the workspace. No requests are sent, nothing is
	// written. Safe to point at a production workspace.
	ScopeReadOnly Scope = "read-only"

	// ScopeRun (default): read + EXECUTE saved requests and load tests. The
	// execution itself still passes the engine's Dispatch policy, so the GUI's
	// approval modal still gates mutating HTTP methods.
	ScopeRun Scope = "run"

	// ScopeWrite: read + run + AUTHOR the workspace (create/update/delete
	// requests and folders, set environment variables). This is the scope that
	// makes "Claude built my collection and I reviewed the git diff" possible,
	// and the one that must be opted into deliberately: these tools rewrite
	// git-tracked YAML on the user's disk.
	ScopeWrite Scope = "write"
)

// ParseScope maps a user-supplied string to a Scope, defaulting to ScopeRun.
// Unknown values are an ERROR rather than a silent downgrade: a typo in
// `--scope wrte` that quietly produced a read+run server would be discovered
// only when an authoring call mysteriously had no tool to call.
func ParseScope(s string) (Scope, error) {
	switch Scope(strings.TrimSpace(strings.ToLower(s))) {
	case "":
		return ScopeRun, nil
	case ScopeReadOnly:
		return ScopeReadOnly, nil
	case ScopeRun:
		return ScopeRun, nil
	case ScopeWrite:
		return ScopeWrite, nil
	default:
		return "", fmt.Errorf("unknown MCP scope %q (want read-only, run, or write)", s)
	}
}

// allowsRun reports whether the scope may execute requests.
func (s Scope) allowsRun() bool { return s == ScopeRun || s == ScopeWrite }

// allowsWrite reports whether the scope may modify the workspace.
func (s Scope) allowsWrite() bool { return s == ScopeWrite }

// WriteIntent describes a pending workspace mutation, in the terms a human
// needs to decide on it — never in the terms the protocol used. "Create
// request 'Refund charge' (POST /v1/charges/:id/refunds)" is a decision;
// `{"tool":"create_request","args":{...}}` is a puzzle.
type WriteIntent struct {
	// Tool is the MCP tool name, for logging and for the host to group by.
	Tool string
	// Summary is one line a person can approve or refuse on sight.
	Summary string
	// WorkspaceID scopes the change; a host may auto-allow some workspaces.
	WorkspaceID string
}

// WriteGuard authorizes workspace mutations.
//
// It is deliberately SEPARATE from core.PolicyEngine. That one gates outbound
// requests and decides on the HTTP method — the question "may this agent send
// a DELETE to production?". This one gates edits to the user's files and
// decides on intent — "may this agent rewrite my collection?". Conflating them
// would mean a workspace edit inheriting an approval prompt that talks about a
// URL it is not going to call.
type WriteGuard interface {
	// AuthorizeWrite returns false to refuse, with a reason the agent sees.
	AuthorizeWrite(ctx context.Context, intent WriteIntent) (bool, string)
}

// AllowAllWrites is the guard for non-interactive hosts (the stdio binary),
// where the human's consent was the act of starting the server with
// --scope=write in the first place. There is nobody to prompt.
type AllowAllWrites struct{}

func (AllowAllWrites) AuthorizeWrite(context.Context, WriteIntent) (bool, string) {
	return true, ""
}

// DenyAllWrites is the safe default when a host installs no guard but a write
// scope somehow reaches the server. It should be unreachable — New() refuses
// that combination — and exists so the failure mode of a future wiring mistake
// is "nothing happens" rather than "an agent silently rewrote the workspace".
type DenyAllWrites struct{}

func (DenyAllWrites) AuthorizeWrite(context.Context, WriteIntent) (bool, string) {
	return false, "this MCP server has no write guard installed"
}
