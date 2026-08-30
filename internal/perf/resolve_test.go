package perf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeK6 drops an executable stub at p, creating parent dirs.
func writeFakeK6(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// isolate puts ResolveK6 in a world with no k6 anywhere: an empty working
// directory (so repoSidecar cannot walk up into this repo's own
// build/sidecars/k6), a private HOME (so ManagedK6Path points somewhere
// scratch), an empty PATH, and no env override. Returns the fake HOME.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config")) // Linux equivalent
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvK6Bin, "")
	t.Chdir(t.TempDir())
	return home
}

func TestResolveK6PrefersManagedDownloadOverPATH(t *testing.T) {
	isolate(t)

	pathDir := t.TempDir()
	writeFakeK6(t, filepath.Join(pathDir, "k6"))
	t.Setenv("PATH", pathDir)

	managed, err := ManagedK6Path()
	if err != nil {
		t.Fatalf("ManagedK6Path() error = %v", err)
	}
	writeFakeK6(t, managed)

	got, err := ResolveK6()
	if err != nil {
		t.Fatalf("ResolveK6() error = %v", err)
	}
	if got != managed {
		t.Errorf("ResolveK6() = %q, want the managed download at %q (pinned version must win over an arbitrary PATH k6)", got, managed)
	}
}

func TestResolveK6FallsBackToPATHWhenNoManagedDownload(t *testing.T) {
	isolate(t)

	pathDir := t.TempDir()
	onPath := writeFakeK6(t, filepath.Join(pathDir, "k6"))
	t.Setenv("PATH", pathDir)

	got, err := ResolveK6()
	if err != nil {
		t.Fatalf("ResolveK6() error = %v", err)
	}
	if got != onPath {
		t.Errorf("ResolveK6() = %q, want the PATH k6 at %q", got, onPath)
	}
}

func TestResolveK6PrefersRepoSidecarOverManagedDownload(t *testing.T) {
	isolate(t)

	// A developer's checkout: build/sidecars/k6 under the working directory
	// must beat a stale self-heal download in Application Support.
	repo := t.TempDir()
	sidecar := writeFakeK6(t, filepath.Join(repo, "build", "sidecars", "k6"))
	t.Chdir(repo)

	managed, err := ManagedK6Path()
	if err != nil {
		t.Fatalf("ManagedK6Path() error = %v", err)
	}
	writeFakeK6(t, managed)

	got, err := ResolveK6()
	if err != nil {
		t.Fatalf("ResolveK6() error = %v", err)
	}
	if got != sidecar {
		t.Errorf("ResolveK6() = %q, want the repo sidecar at %q", got, sidecar)
	}
}

func TestResolveK6EnvOverrideBeatsEverything(t *testing.T) {
	isolate(t)

	managed, err := ManagedK6Path()
	if err != nil {
		t.Fatalf("ManagedK6Path() error = %v", err)
	}
	writeFakeK6(t, managed)

	override := writeFakeK6(t, filepath.Join(t.TempDir(), "my-k6"))
	t.Setenv(EnvK6Bin, override)

	got, err := ResolveK6()
	if err != nil {
		t.Fatalf("ResolveK6() error = %v", err)
	}
	if got != override {
		t.Errorf("ResolveK6() = %q, want the %s override at %q", got, EnvK6Bin, override)
	}
}

func TestResolveK6IgnoresNonExecutableManagedFile(t *testing.T) {
	isolate(t)

	managed, err := ManagedK6Path()
	if err != nil {
		t.Fatalf("ManagedK6Path() error = %v", err)
	}
	// A partially written / non-executable file must not be handed to exec.
	if err := os.MkdirAll(filepath.Dir(managed), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(managed, []byte("truncated"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got, err := ResolveK6(); err == nil {
		t.Errorf("ResolveK6() = %q, want an error for a non-executable managed file", got)
	}
}

func TestResolveK6ErrorMentionsTheDownloadOption(t *testing.T) {
	isolate(t)

	_, err := ResolveK6()
	if err == nil {
		t.Fatal("expected an error when no k6 exists anywhere")
	}
	// The message is surfaced verbatim in the load-test panel.
	for _, want := range []string{EnvK6Bin, "download", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}
