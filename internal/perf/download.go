package perf

// Self-heal download for the k6 sidecar.
//
// AGPL boundary (same rule as the rest of this package): what follows fetches
// an OFFICIAL, UNMODIFIED k6 release binary over HTTPS and drops it on disk as
// a standalone file that we later exec. Nothing here links, embeds, or
// rewrites k6 — it is the network equivalent of the user downloading k6
// themselves, which is exactly why it does not taint this app's license.
//
// Every artifact is pinned: one k6 version, one URL per platform, and two
// SHA-256 digests per platform (the release archive and the k6 binary inside
// it). A download that does not match both is discarded, so a compromised or
// truncated fetch can never end up as an executable AUK runs.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// K6Version is the single pinned k6 release AUK ships and downloads. It is
// deliberately the same version the perf runner's NDJSON/handleSummary parsing
// was written and tested against (internal/perf/runner.go) — bumping it means
// re-verifying that parsing, not just swapping a string.
const K6Version = "v0.54.0"

const k6ReleaseBaseURL = "https://github.com/grafana/k6/releases/download"

// k6ArchiveSizeLimit caps how much we are willing to write to disk for a
// release archive (~29MB today) so a redirect to something enormous cannot
// fill the user's disk before the checksum ever gets a say.
const k6ArchiveSizeLimit = 128 << 20 // 128 MiB

// k6Release is one pinned upstream release asset. Target is the upstream
// target triple ("macos-arm64"), which drives both the archive name and the
// directory name inside it.
type k6Release struct {
	Target        string
	BinName       string
	ArchiveSHA256 string
	BinarySHA256  string
}

// k6Releases maps GOOS/GOARCH to the pinned upstream asset. Only zip-packaged
// targets are listed: upstream ships Linux as .tar.gz, and AUK is not
// distributed for Linux, so a Linux user gets the "install k6 yourself" error
// rather than a second, barely exercised extraction path. build/sidecars/
// download-k6.sh does handle Linux, for developers.
var k6Releases = map[string]k6Release{
	"darwin/arm64": {
		Target:        "macos-arm64",
		BinName:       "k6",
		ArchiveSHA256: "9fb42e1343d28fc26e6efa1269283edf39ddc20767249869c84aa333741fc3ae",
		BinarySHA256:  "4e01b00234ede54382877df9dd9cfa2813af383235e6d253c776136a4687126e",
	},
	"darwin/amd64": {
		Target:        "macos-amd64",
		BinName:       "k6",
		ArchiveSHA256: "244ce603e3e1f0081b5b0b444de5631c22d0204ffbfa8b7f13ea6316da1f4376",
		BinarySHA256:  "021a0b693b371ec6b23e315ff0e424cfa3429379708c570f12113717ca8acd14",
	},
	"windows/amd64": {
		Target:        "windows-amd64",
		BinName:       "k6.exe",
		ArchiveSHA256: "b1b1221c31b82f81b95f67c0041c8067c9cf49017b0eb05ecaafd05f330a2dac",
		BinarySHA256:  "f732b5b9234d6daabe6e9f0d51908056e0da21a4e68892b0347daebd1cc0c13e",
	},
}

// archiveName is the release asset's file name, e.g. k6-v0.54.0-macos-arm64.zip.
func (r k6Release) archiveName() string {
	return fmt.Sprintf("k6-%s-%s.zip", K6Version, r.Target)
}

// URL is the official GitHub release download URL for this asset.
func (r k6Release) URL() string {
	return fmt.Sprintf("%s/%s/%s", k6ReleaseBaseURL, K6Version, r.archiveName())
}

// binPathInArchive is the slash-separated path of the k6 binary inside the
// archive: upstream nests it one directory deep, named after the asset.
func (r k6Release) binPathInArchive() string {
	return path.Join(fmt.Sprintf("k6-%s-%s", K6Version, r.Target), r.BinName)
}

// releaseFor returns the pinned release for a GOOS/GOARCH pair.
func releaseFor(goos, goarch string) (k6Release, bool) {
	r, ok := k6Releases[goos+"/"+goarch]
	return r, ok
}

// ManagedK6Path is where a self-heal download lands:
// ~/Library/Application Support/AUK/bin/k6 on macOS (os.UserConfigDir is
// exactly Application Support there). It is outside the .app bundle on
// purpose — the bundle is code-signed and read-only in a normal install, and
// writing into it would break the signature.
func ManagedK6Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the user application-support directory: %w", err)
	}
	name := "k6"
	if runtime.GOOS == "windows" {
		name = "k6.exe"
	}
	return filepath.Join(dir, "AUK", "bin", name), nil
}

// DownloadK6 fetches the pinned k6 release for this platform, verifies it
// against both pinned digests, and installs it at ManagedK6Path. It returns
// the installed path. It is safe to call when a k6 is already installed there
// — the file is simply replaced.
//
// The caller owns the timeout via ctx; this is a ~29MB download.
func DownloadK6(ctx context.Context) (string, error) {
	rel, ok := releaseFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return "", fmt.Errorf("no pinned k6 %s download for %s/%s — install k6 yourself (https://k6.io/docs/get-started/installation/) and AUK will find it on your PATH",
			K6Version, runtime.GOOS, runtime.GOARCH)
	}

	dest, err := ManagedK6Path()
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "auk-k6-")
	if err != nil {
		return "", fmt.Errorf("cannot create a temporary download directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archive := filepath.Join(tmpDir, rel.archiveName())
	if err := downloadFile(ctx, rel.URL(), archive); err != nil {
		return "", err
	}
	if err := installK6FromArchive(archive, rel, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// downloadFile streams url to dest, refusing anything over
// k6ArchiveSizeLimit. Integrity is the caller's job (installK6FromArchive
// checks the pinned digest) — this only has to not be a footgun.
func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("bad download URL %q: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: server returned %s", url, resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("cannot write the download: %w", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, k6ArchiveSizeLimit+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("downloading %s: %w", url, copyErr)
	case closeErr != nil:
		return fmt.Errorf("cannot write the download: %w", closeErr)
	case n > k6ArchiveSizeLimit:
		return fmt.Errorf("downloading %s: archive is larger than the %d-byte limit", url, int64(k6ArchiveSizeLimit))
	}
	return nil
}

// installK6FromArchive verifies the downloaded archive against its pinned
// digest, extracts the single k6 binary, verifies THAT against its own pinned
// digest, and only then moves it into place at dest with mode 0755.
//
// Verifying twice is not paranoia theatre: the archive digest catches a bad
// transfer, and the binary digest catches a zip whose entry differs from the
// one we think we pinned (a bad path match, a zip-slip attempt, a rebuilt
// archive). Nothing becomes executable at dest until both agree.
func installK6FromArchive(archivePath string, rel k6Release, dest string) error {
	if err := verifySHA256(archivePath, rel.ArchiveSHA256); err != nil {
		return fmt.Errorf("k6 %s archive failed verification: %w", K6Version, err)
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("cannot read the k6 archive: %w", err)
	}
	defer zr.Close()

	entry := findBinaryEntry(zr, rel)
	if entry == nil {
		return fmt.Errorf("k6 archive does not contain %s", rel.binPathInArchive())
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(dest), err)
	}

	// Extract beside the destination (same filesystem) so the final install is
	// a rename — an interrupted download can never leave a half-written binary
	// where ResolveK6 would find and exec it.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".k6-download-*")
	if err != nil {
		return fmt.Errorf("cannot stage the k6 binary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	rc, err := entry.Open()
	if err != nil {
		tmp.Close()
		return fmt.Errorf("cannot read k6 out of the archive: %w", err)
	}
	_, copyErr := io.Copy(tmp, io.LimitReader(rc, k6ArchiveSizeLimit))
	rc.Close()
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("cannot extract the k6 binary: %w", copyErr)
	}

	if err := verifySHA256(tmpName, rel.BinarySHA256); err != nil {
		return fmt.Errorf("extracted k6 %s binary failed verification: %w", K6Version, err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("cannot make k6 executable: %w", err)
	}
	// On POSIX the rename replaces any existing k6 atomically. Windows refuses
	// to rename onto an existing file, so remove first there — and only there,
	// so a rename failure can never leave the user with no k6 at all.
	if runtime.GOOS == "windows" {
		_ = os.Remove(dest)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("cannot install k6 at %s: %w", dest, err)
	}
	return nil
}

// findBinaryEntry prefers the exact pinned path inside the archive and falls
// back to any file whose base name matches, so a harmless upstream change to
// the wrapper directory name does not break the download. The binary digest
// check downstream is what makes the fallback safe.
func findBinaryEntry(zr *zip.ReadCloser, rel k6Release) *zip.File {
	want := rel.binPathInArchive()
	var fallback *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if name == want {
			return f
		}
		if fallback == nil && path.Base(name) == rel.BinName {
			fallback = f
		}
	}
	return fallback
}

// verifySHA256 hashes the file at p and compares it to the pinned hex digest.
func verifySHA256(p, want string) error {
	if want == "" {
		return errors.New("no pinned SHA-256 to verify against")
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", strings.ToLower(want), got)
	}
	return nil
}
