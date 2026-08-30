package perf

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sha256Hex is the digest the pinned constants are expressed in.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fixtureArchive writes a zip laid out exactly like an upstream k6 release
// (one wrapper directory containing the binary) and returns its path plus a
// k6Release whose digests match it, so extract+verify can be exercised with no
// network.
func fixtureArchive(t *testing.T, target, binName string, payload []byte) (string, k6Release) {
	t.Helper()

	rel := k6Release{
		Target:       target,
		BinName:      binName,
		BinarySHA256: sha256Hex(payload),
	}

	p := filepath.Join(t.TempDir(), rel.archiveName())
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create fixture archive: %v", err)
	}
	zw := zip.NewWriter(f)
	// A directory entry, as upstream's archive has, to prove it is skipped.
	if _, err := zw.Create(strings.TrimSuffix(rel.binPathInArchive(), binName)); err != nil {
		t.Fatalf("write dir entry: %v", err)
	}
	w, err := zw.Create(rel.binPathInArchive())
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture archive: %v", err)
	}
	rel.ArchiveSHA256 = sha256Hex(raw)
	return p, rel
}

func TestInstallK6FromArchiveVerifiesAndInstalls(t *testing.T) {
	payload := []byte("#!/bin/sh\necho k6 v0.54.0\n")
	archive, rel := fixtureArchive(t, "macos-arm64", "k6", payload)

	// A nested destination proves the parent directories get created.
	dest := filepath.Join(t.TempDir(), "AUK", "bin", "k6")
	if err := installK6FromArchive(archive, rel, dest); err != nil {
		t.Fatalf("installK6FromArchive() error = %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed k6: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("installed binary content = %q, want %q", got, payload)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat installed k6: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("installed mode = %v, want 0755", info.Mode().Perm())
	}
	if !isExecutable(dest) {
		t.Errorf("installed k6 is not executable, so ResolveK6 would skip it")
	}

	// No temp staging files may be left behind next to the install.
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("read install dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "k6" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("install dir = %v, want exactly [k6]", names)
	}
}

func TestInstallK6FromArchiveRejectsBadArchiveDigest(t *testing.T) {
	archive, rel := fixtureArchive(t, "macos-arm64", "k6", []byte("k6-payload"))
	rel.ArchiveSHA256 = strings.Repeat("ab", 32) // pinned digest says something else

	dest := filepath.Join(t.TempDir(), "k6")
	err := installK6FromArchive(archive, rel, dest)
	if err == nil {
		t.Fatal("expected a verification error for a mismatched archive digest")
	}
	if !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("error = %v, want a SHA-256 mismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("a failed archive verification must not install anything at %s", dest)
	}
}

func TestInstallK6FromArchiveRejectsBadBinaryDigest(t *testing.T) {
	archive, rel := fixtureArchive(t, "macos-arm64", "k6", []byte("k6-payload"))
	// Archive digest is right, the binary inside is not what we pinned — the
	// case a re-cut release or a swapped zip entry would produce.
	rel.BinarySHA256 = strings.Repeat("cd", 32)

	dest := filepath.Join(t.TempDir(), "k6")
	err := installK6FromArchive(archive, rel, dest)
	if err == nil {
		t.Fatal("expected a verification error for a mismatched binary digest")
	}
	if !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("error = %v, want a SHA-256 mismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("a failed binary verification must not install anything at %s", dest)
	}
	// And nothing half-written may remain in the destination directory.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 0 {
		t.Errorf("destination dir should be empty after a failed install, has %d entries", len(entries))
	}
}

func TestInstallK6FromArchiveReplacesExistingBinary(t *testing.T) {
	payload := []byte("new k6")
	archive, rel := fixtureArchive(t, "macos-arm64", "k6", payload)

	dest := filepath.Join(t.TempDir(), "k6")
	if err := os.WriteFile(dest, []byte("stale k6"), 0o755); err != nil {
		t.Fatalf("seed stale binary: %v", err)
	}
	if err := installK6FromArchive(archive, rel, dest); err != nil {
		t.Fatalf("installK6FromArchive() error = %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("stale binary was not replaced: %q", got)
	}
}

func TestInstallK6FromArchiveMissingEntry(t *testing.T) {
	archive, rel := fixtureArchive(t, "macos-arm64", "k6", []byte("k6-payload"))
	// Look for a binary name the archive does not contain.
	rel.BinName = "not-k6"

	err := installK6FromArchive(archive, rel, filepath.Join(t.TempDir(), "k6"))
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("error = %v, want a 'does not contain' error", err)
	}
}

func TestPinnedReleaseURLsAndDigests(t *testing.T) {
	// Guards the shape of every pinned entry: a typo in a target or digest
	// would otherwise only surface as a failed download on a user's machine.
	if !strings.HasPrefix(K6Version, "v") {
		t.Errorf("K6Version = %q, want a leading v", K6Version)
	}
	for key, rel := range k6Releases {
		for _, d := range []struct{ what, digest string }{
			{"archive", rel.ArchiveSHA256},
			{"binary", rel.BinarySHA256},
		} {
			if len(d.digest) != 64 {
				t.Errorf("%s: %s digest %q is not 64 hex chars", key, d.what, d.digest)
			}
			if _, err := hex.DecodeString(d.digest); err != nil {
				t.Errorf("%s: %s digest is not hex: %v", key, d.what, err)
			}
		}
		if rel.ArchiveSHA256 == rel.BinarySHA256 {
			t.Errorf("%s: archive and binary digests are identical — likely a copy/paste error", key)
		}
		want := "https://github.com/grafana/k6/releases/download/" + K6Version + "/k6-" + K6Version + "-" + rel.Target + ".zip"
		if rel.URL() != want {
			t.Errorf("%s: URL() = %q, want %q", key, rel.URL(), want)
		}
	}

	// The platform AUK actually ships must be pinned.
	if _, ok := releaseFor("darwin", "arm64"); !ok {
		t.Error("darwin/arm64 must have a pinned k6 release")
	}
	if _, ok := releaseFor("plan9", "riscv64"); ok {
		t.Error("releaseFor should not invent a release for an unpinned platform")
	}
}

func TestManagedK6PathIsUnderApplicationSupport(t *testing.T) {
	p, err := ManagedK6Path()
	if err != nil {
		t.Fatalf("ManagedK6Path() error = %v", err)
	}
	name := "k6"
	if runtime.GOOS == "windows" {
		name = "k6.exe"
	}
	if filepath.Base(p) != name {
		t.Errorf("ManagedK6Path() = %q, want it to end in %s", p, name)
	}
	if filepath.Base(filepath.Dir(p)) != "bin" || filepath.Base(filepath.Dir(filepath.Dir(p))) != "AUK" {
		t.Errorf("ManagedK6Path() = %q, want .../AUK/bin/%s", p, name)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("ManagedK6Path() = %q, want an absolute path", p)
	}
}
