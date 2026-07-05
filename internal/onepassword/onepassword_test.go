package onepassword

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestIsRef(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"op://vault/item/field", true},
		{"op://Employee/GitHub/token", true},
		{"", false},
		{"plain-value", false},
		{"${cookie(name)}", false},
		{"https://op.example.com", false},
		{" op://vault/item/field", false}, // leading whitespace is a different (invalid) value, not a ref
	}
	for _, c := range cases {
		if got := IsRef(c.value); got != c.want {
			t.Errorf("IsRef(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}

// TestRead_AbsentCLI_ReturnsActionableError forces the "op not on PATH" path
// deterministically (regardless of whether whatever machine runs this suite
// happens to have op installed) by pointing PATH at an empty temp dir. This
// is the one guaranteed-reproducible-everywhere test for this package: op
// itself is confirmed NOT installed in the dev environment this was written
// in, and there's no test 1Password vault available to exercise a real
// `op read` against, so the graceful-absence path is what's actually
// verified here rather than a live secret fetch.
func TestRead_AbsentCLI_ReturnsActionableError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if Available() {
		t.Fatal("Available() = true with PATH pointed at an empty directory; test setup is broken")
	}

	_, err := Read(context.Background(), "op://vault/item/field")
	if err == nil {
		t.Fatal("Read: want an error when op isn't on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("Read error = %q, want it to mention op isn't on PATH", err.Error())
	}
	if !strings.Contains(err.Error(), "op://vault/item/field") {
		t.Fatalf("Read error = %q, want it to echo the ref that couldn't be resolved", err.Error())
	}
}

// TestAvailable_MatchesLookPath exercises the actual host PATH (not an
// empty override): true on machines with op installed, false everywhere
// else, but either way it must equal a direct exec.LookPath check, since
// Available() is meant to be a thin, faithful wrapper over exactly that.
func TestAvailable_MatchesLookPath(t *testing.T) {
	_, lookErr := exec.LookPath("op")
	want := lookErr == nil
	if got := Available(); got != want {
		t.Fatalf("Available() = %v, want %v (to match exec.LookPath(%q))", got, want, "op")
	}
}
