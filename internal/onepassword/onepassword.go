// Package onepassword resolves op://vault/item/field secret references
// through the user's own 1Password CLI (`op`) — installed and signed in
// separately by the user, never bundled or vendored by AUK. Unlike k6
// (internal/perf), which AUK ships a copy of, `op` is a per-user
// authenticated proprietary tool tied to someone's own 1Password account, so
// the only sane integration is "shell out to whatever's on PATH, and say so
// clearly when it's missing" — no SDK, no embedded credentials, no Connect
// server token to manage.
package onepassword

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// refPrefix is how 1Password's own CLI/app denote a secret reference
// (op://vault/item/field) — the same syntax `op read` accepts directly.
const refPrefix = "op://"

// IsRef reports whether s looks like a 1Password secret reference, so
// callers can decide to resolve it instead of using it as a literal value.
func IsRef(s string) bool {
	return strings.HasPrefix(s, refPrefix)
}

// Available reports whether the op CLI is on PATH. Cheap enough to call on
// every check — it's a PATH lookup, not a subprocess spawn.
func Available() bool {
	_, err := exec.LookPath("op")
	return err == nil
}

// Read resolves ref (an op://vault/item/field reference) to its live value
// via `op read`. Requires the user to already be signed in through op's own
// session (outside this app's control); a stale/missing session surfaces
// op's own stderr text unchanged, since op's own messaging already tells the
// user what to do (`op signin`, etc.) better than this package could guess.
func Read(ctx context.Context, ref string) (string, error) {
	if !Available() {
		return "", fmt.Errorf("1Password CLI (op) not found on PATH — install it from https://developer.1password.com/docs/cli to resolve %s", ref)
	}

	cmd := exec.CommandContext(ctx, "op", "read", ref)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("op read %s: %s", ref, msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}
