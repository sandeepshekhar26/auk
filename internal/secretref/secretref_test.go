package secretref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSchemeDetection(t *testing.T) {
	r := Default("/tmp/ws")
	cases := map[string]bool{
		"op://vault/item/field":       true,
		"env://.env#KEY":              true,
		// A URL in a variable is a value, not a reference. Treating
		// https:// as a scheme would make every base-URL variable
		// unresolvable.
		"https://api.example.com":     false,
		"postgres://user:pw@host/db":  false, // unregistered scheme: a literal
		"plain-value":                 false,
		"":                            false,
		"not a scheme://x":            false,
		"://x":                        false,
	}
	for in, want := range cases {
		if got := r.IsRef(in); got != want {
			t.Errorf("IsRef(%q) = %v, want %v", in, got, want)
		}
	}
}

// An unregistered scheme must pass through untouched rather than erroring —
// a Postgres DSN in a variable is a perfectly good literal value.
func TestUnregisteredSchemeIsALiteral(t *testing.T) {
	r := Default("/tmp/ws")
	const dsn = "postgres://user:pw@host/db"
	got, err := r.Resolve(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != dsn {
		t.Errorf("Resolve(%q) = %q, want it unchanged", dsn, got)
	}
}

func TestDotEnvResolvesTheUsualSyntax(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), strings.Join([]string{
		"# a comment",
		"",
		"PLAIN=hello",
		"export EXPORTED=from-export",
		`QUOTED="  spaced  value  "`,
		"SINGLE='single quoted'",
		"TRAILING=value # not part of it",
		`WITH_NEWLINE="line1\nline2"`,
		"EQUALS_IN_VALUE=a=b=c",
		"EMPTY=",
	}, "\n"))

	r := Default(dir)
	ctx := context.Background()
	cases := map[string]string{
		"env://.env#PLAIN":           "hello",
		"env://.env#EXPORTED":        "from-export",
		"env://.env#QUOTED":          "  spaced  value  ",
		"env://.env#SINGLE":          "single quoted",
		"env://.env#TRAILING":        "value",
		"env://.env#WITH_NEWLINE":    "line1\nline2",
		"env://.env#EQUALS_IN_VALUE": "a=b=c",
		"env://.env#EMPTY":           "",
		// An omitted path defaults to .env, the overwhelmingly common case.
		"env://#PLAIN": "hello",
	}
	for ref, want := range cases {
		got, err := r.Resolve(ctx, ref)
		if err != nil {
			t.Errorf("Resolve(%q): %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", ref, got, want)
		}
	}
}

// The security boundary: a relative path must not be able to climb out of the
// workspace and read arbitrary files. Checked AFTER cleaning, because the
// literal string "../../etc/passwd" looks relative right up until it isn't.
func TestDotEnvRefusesToEscapeTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "OK=yes")
	// A file the reference will try to reach, outside the workspace.
	outside := filepath.Join(filepath.Dir(dir), "outside.env")
	writeFile(t, outside, "STOLEN=secret-from-outside")
	t.Cleanup(func() { os.Remove(outside) })

	r := Default(dir)
	for _, ref := range []string{
		"env://../outside.env#STOLEN",
		"env://./../outside.env#STOLEN",
		"env://subdir/../../outside.env#STOLEN",
	} {
		got, err := r.Resolve(context.Background(), ref)
		if err == nil {
			t.Errorf("Resolve(%q) escaped the workspace and returned %q", ref, got)
			continue
		}
		if !strings.Contains(err.Error(), "escapes the workspace") {
			t.Errorf("Resolve(%q) failed with %v; want the escape refusal", ref, err)
		}
	}

	// An ABSOLUTE path is allowed — it is an explicit, visible choice by the
	// person who typed it, not a traversal.
	abs := "env://" + outside + "#STOLEN"
	if got, err := r.Resolve(context.Background(), abs); err != nil || got != "secret-from-outside" {
		t.Errorf("absolute path = %q, %v; want it permitted", got, err)
	}
}

// With no workspace anchor, a relative path must be refused rather than
// resolved against whatever the process's cwd happens to be.
func TestDotEnvRefusesRelativePathsWithNoAnchor(t *testing.T) {
	r := Default("")
	_, err := r.Resolve(context.Background(), "env://.env#ANY")
	if err == nil {
		t.Fatal("a relative path resolved with no workspace directory")
	}
	if !strings.Contains(err.Error(), "workspace directory") {
		t.Errorf("err = %v; want it to name the missing anchor", err)
	}
}

func TestDotEnvErrorsAreActionable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "PRESENT=1")
	r := Default(dir)
	ctx := context.Background()

	if _, err := r.Resolve(ctx, "env://.env#MISSING"); err == nil ||
		!strings.Contains(err.Error(), "MISSING") {
		t.Errorf("missing key error = %v; it should name the key", err)
	}
	if _, err := r.Resolve(ctx, "env://nope.env#X"); err == nil ||
		!strings.Contains(err.Error(), "no .env file") {
		t.Errorf("missing file error = %v; it should say the file is absent", err)
	}
	// A reference with no #KEY is a typo worth naming precisely, since the
	// value would otherwise silently be the whole file.
	if _, err := r.Resolve(ctx, "env://.env"); err == nil ||
		!strings.Contains(err.Error(), "#KEY") {
		t.Errorf("missing key part error = %v; it should show the right syntax", err)
	}
}

// A duplicate scheme must panic at startup rather than silently shadow, since
// which resolver wins would otherwise depend on registration order.
func TestDuplicateSchemePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate scheme did not panic")
		}
	}()
	r := New()
	r.Register(OnePassword{})
	r.Register(OnePassword{})
}

// A resolver whose tool is missing must report HOW to fix it — the user is one
// sentence away from a working setup and deserves that sentence.
func TestUnavailableResolverExplainsItself(t *testing.T) {
	err := OnePassword{}.Available()
	if err == nil {
		t.Skip("op is installed on this machine; nothing to assert")
	}
	if !strings.Contains(err.Error(), "1Password CLI") || !strings.Contains(err.Error(), "http") {
		t.Errorf("err = %v; want it to name the tool and where to get it", err)
	}
}
