package updater

// The security core: download → verify → stage → swap.
//
// This mirrors the rigor of internal/perf/download.go (the k6 self-heal) but
// points it at a GitHub release DMG and adds the check that actually matters
// for code that installs an executable: the downloaded .app must be signed by
// OUR Developer ID and notarized by Apple.
//
// VERIFICATION CHAIN, in order, nothing trusted until all pass:
//  1. Size-capped, timeout-bounded download (a redirect to something enormous
//     can't fill the disk; a hang can't wedge the UI).
//  2. SHA-256, IF the release notes published one (parseSHA256FromBody). This
//     catches a corrupt/truncated transfer. It is deliberately NOT the trust
//     anchor: an attacker who could serve a malicious DMG could serve a
//     matching hash in notes they also control. It's a bonus integrity check.
//  3. codesign --verify --deep --strict: the bundle's signature is intact.
//  4. TeamIdentifier == V8SAC4GCQQ: it's OURS. This is the real anti-tamper
//     guarantee — Apple's notary refuses to notarize anything not signed by a
//     valid Developer ID, and the private key for ours never leaves the
//     signing machine, so a swapped-in malicious app cannot both be notarized
//     AND carry our Team ID.
//  5. spctl --assess --type exec: Gatekeeper accepts it as Notarized Developer
//     ID (the stapled notarization ticket is present and valid).
// Any failure → reject, detach, install nothing.
//
// The exec/mount/filesystem edges sit behind the `runner` interface so the
// pure decision logic (parse a Team ID, decide "accepted", find the mount
// point / the .app / the bundle root) is unit-tested without hdiutil or a
// real signed bundle in the loop.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// DefaultTeamID is AUK's Apple Developer Team ID. Anything not signed by it is
// rejected, notarized or not.
const DefaultTeamID = "V8SAC4GCQQ"

// dmgSizeLimit caps the DMG download. The v0.3.0 DMG is ~42 MB; 256 MiB is
// generous headroom while still refusing a hostile redirect to a huge file.
const dmgSizeLimit = 256 << 20

// runner runs an external command and returns its stdout and stderr
// separately (codesign and spctl write their useful output to stderr). The
// real implementation is execRunner; tests substitute a stub.
type runner interface {
	run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return []byte(out.String()), []byte(errBuf.String()), err
}

// StagedUpdate is the outcome of a successful download+verify: a verified .app
// copied to a stable staging path, plus the verified DMG kept alongside it so
// the guided-install fallback can reveal it.
type StagedUpdate struct {
	Version   string `json:"version"`
	AppPath   string `json:"appPath"`
	DMGPath   string `json:"dmgPath"`
	StagedDir string `json:"stagedDir"`
}

// pendingDirFor is the staging location under the app-support dir.
func pendingDirFor(configDir string) string {
	return filepath.Join(configDir, "AUK", "pending-update")
}

func pendingDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return pendingDirFor(dir), nil
}

// downloadDMG streams url to dest, refusing anything past dmgSizeLimit. Mirror
// of perf/download.go's downloadFile: integrity is a later step's job, this
// only has to be safe.
func downloadDMG(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("bad download URL %q: %w", url, err)
	}
	req.Header.Set("User-Agent", "AUK-Updater")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
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
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, dmgSizeLimit+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("downloading %s: %w", url, copyErr)
	case closeErr != nil:
		return fmt.Errorf("cannot write the download: %w", closeErr)
	case n > dmgSizeLimit:
		return fmt.Errorf("downloading %s: DMG is larger than the %d-byte limit", url, int64(dmgSizeLimit))
	}
	return nil
}

// verifySHA256 hashes p and compares it to the pinned hex digest (lower-cased,
// case-insensitive compare). Local copy of the perf-package check so the
// updater is self-contained.
func verifySHA256(p, want string) error {
	if want == "" {
		return fmt.Errorf("no SHA-256 to verify against")
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

// attachDMG mounts dmgPath read-only with no Finder window and returns the
// /Volumes mount point.
func attachDMG(ctx context.Context, r runner, dmgPath string) (string, error) {
	stdout, stderr, err := r.run(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noverify", dmgPath)
	if err != nil {
		return "", fmt.Errorf("mount DMG: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	mount := parseMountPoint(string(stdout))
	if mount == "" {
		return "", fmt.Errorf("mount DMG: could not find a mount point in hdiutil output")
	}
	return mount, nil
}

// detachDMG unmounts mountPoint, best-effort with a forced retry. A failure to
// detach is not fatal to an install that already succeeded, but is surfaced so
// the caller can log it.
func detachDMG(ctx context.Context, r runner, mountPoint string) error {
	if mountPoint == "" {
		return nil
	}
	if _, _, err := r.run(ctx, "hdiutil", "detach", mountPoint); err == nil {
		return nil
	}
	_, stderr, err := r.run(ctx, "hdiutil", "detach", "-force", mountPoint)
	if err != nil {
		return fmt.Errorf("detach %s: %w: %s", mountPoint, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// parseMountPoint extracts the /Volumes mount path from `hdiutil attach`
// tab-delimited text output. Each line is "dev \t type \t mountpoint"; the
// mount point is the last field that starts with "/Volumes/". Pure/testable.
func parseMountPoint(hdiutilStdout string) string {
	for _, line := range strings.Split(hdiutilStdout, "\n") {
		fields := strings.Split(line, "\t")
		last := strings.TrimSpace(fields[len(fields)-1])
		if strings.HasPrefix(last, "/Volumes/") {
			return last
		}
	}
	return ""
}

// findAppBundle returns the single .app inside a mounted volume directory.
func findAppBundle(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	name := findAppInEntries(names)
	if name == "" {
		return "", fmt.Errorf("no .app found in %s", dir)
	}
	return filepath.Join(dir, name), nil
}

// findAppInEntries returns the first ".app" entry (ignoring the "Applications"
// drag-target symlink DMGs usually include). Pure/testable.
func findAppInEntries(names []string) string {
	for _, n := range names {
		if strings.HasSuffix(n, ".app") {
			return n
		}
	}
	return ""
}

// verifyAppBundle runs the full signature/notarization/Team-ID gate on appPath
// against wantTeamID. Returns nil only when the bundle is intact, ours, and
// notarized.
func verifyAppBundle(ctx context.Context, r runner, appPath, wantTeamID string) error {
	// 3. Signature integrity.
	if _, stderr, err := r.run(ctx, "codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath); err != nil {
		return fmt.Errorf("code signature invalid: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	// 4. Team ID is ours. codesign -dvvv writes "TeamIdentifier=..." to stderr.
	_, infoErr, err := r.run(ctx, "codesign", "-dvvv", appPath)
	if err != nil {
		return fmt.Errorf("cannot read code signature: %w", err)
	}
	gotTeam := parseTeamID(string(infoErr))
	if gotTeam == "" {
		return fmt.Errorf("code signature has no Team Identifier")
	}
	if !strings.EqualFold(gotTeam, wantTeamID) {
		return fmt.Errorf("refusing update: signed by Team ID %q, expected %q", gotTeam, wantTeamID)
	}

	// 5. Notarization / Gatekeeper acceptance.
	spOut, spErr, err := r.run(ctx, "spctl", "--assess", "--type", "exec", "--verbose=4", appPath)
	assessment := string(spOut) + string(spErr)
	if err != nil {
		return fmt.Errorf("notarization check failed (Gatekeeper rejected the app): %s", strings.TrimSpace(assessment))
	}
	if !notarizationAccepted(assessment) {
		return fmt.Errorf("app is not notarized by Developer ID: %s", strings.TrimSpace(assessment))
	}
	return nil
}

// parseTeamID pulls the value out of a "TeamIdentifier=XXXXXXXXXX" line in
// codesign -dvvv output. Returns "" if absent or "not set". Pure/testable.
func parseTeamID(codesignOutput string) string {
	for _, line := range strings.Split(codesignOutput, "\n") {
		line = strings.TrimSpace(line)
		const key = "TeamIdentifier="
		if strings.HasPrefix(line, key) {
			v := strings.TrimSpace(strings.TrimPrefix(line, key))
			if v == "" || strings.EqualFold(v, "not set") {
				return ""
			}
			return v
		}
	}
	return ""
}

// notarizationAccepted reports whether spctl assessed the bundle as an
// accepted, notarized Developer ID app. Requires BOTH the "accepted" verdict
// and a "Notarized Developer ID" source — a merely ad-hoc or
// Developer-ID-signed-but-not-notarized app must not pass. Pure/testable.
func notarizationAccepted(spctlOutput string) bool {
	lower := strings.ToLower(spctlOutput)
	if !strings.Contains(lower, "accepted") {
		return false
	}
	return strings.Contains(lower, "notarized developer id")
}

// appBundleRoot returns the ".app" directory that contains the running
// executable exe (.../AUK.app/Contents/MacOS/AUK → .../AUK.app), or "" when
// exe is not inside a bundle. Pure/testable.
func appBundleRoot(exe string) string {
	dir := filepath.Dir(exe)
	for i := 0; i < 6 && dir != "" && dir != string(filepath.Separator); i++ {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// currentAppBundle returns the running app's .app root, resolving symlinks.
func currentAppBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	root := appBundleRoot(exe)
	if root == "" {
		return "", fmt.Errorf("not running from a .app bundle (dev build?) — %s", exe)
	}
	return root, nil
}

// stageApp copies srcApp (the verified .app inside the mounted DMG) to
// stagedPath using ditto, which preserves the extended attributes the code
// signature depends on — a plain recursive copy would strip them and break the
// signature we just verified.
func stageApp(ctx context.Context, r runner, srcApp, stagedPath string) error {
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(stagedPath)
	if _, stderr, err := r.run(ctx, "ditto", srcApp, stagedPath); err != nil {
		return fmt.Errorf("stage app: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// dirWritable reports whether we can create files in dir (i.e. can replace a
// bundle living there). Used to choose the one-click swap vs the guided
// fallback.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".auk-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// swapHelperScript is the detached shell helper that performs the actual
// bundle replacement AFTER the app has quit. It waits for our PID to exit
// (so the running bundle is never modified underfoot), moves the old bundle
// aside atomically, ditto-copies the staged bundle into place (preserving the
// signature), relaunches, and cleans up. On any failure it rolls the old
// bundle back so the user is never left with no app.
const swapHelperScript = `#!/bin/sh
# AUK auto-update swap helper. Args: <pid> <staged_app> <dest_app>
pid="$1"; staged="$2"; dest="$3"
i=0
while kill -0 "$pid" 2>/dev/null; do
  sleep 0.2
  i=$((i+1))
  [ "$i" -gt 300 ] && break
done
sleep 0.3
rm -rf "$dest.old" 2>/dev/null
[ -d "$dest" ] && mv "$dest" "$dest.old" 2>/dev/null
if ditto "$staged" "$dest"; then
  rm -rf "$dest.old" 2>/dev/null
  rm -rf "$(dirname "$staged")" 2>/dev/null
  open "$dest"
else
  rm -rf "$dest" 2>/dev/null
  [ -d "$dest.old" ] && mv "$dest.old" "$dest"
  open "$dest"
fi
`

// spawnSwapAndRelaunch writes the helper script into scriptDir and starts it
// DETACHED (its own session, so it survives this process exiting), passing our
// PID, the staged bundle, and the destination bundle. The caller must quit the
// app immediately afterwards; the helper waits for that exit, then swaps and
// relaunches.
func spawnSwapAndRelaunch(scriptDir, stagedApp, destApp string) error {
	script := filepath.Join(scriptDir, "auk-swap.sh")
	if err := os.WriteFile(script, []byte(swapHelperScript), 0o755); err != nil {
		return fmt.Errorf("write update helper: %w", err)
	}
	pid := fmt.Sprintf("%d", os.Getpid())
	cmd := exec.Command("/bin/sh", script, pid, stagedApp, destApp)
	// New session: detach from the app's process group so quitting the app
	// doesn't take the helper down with it (SIGHUP), and it can outlive us.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch update helper: %w", err)
	}
	// Reap nothing — we're about to exit; releasing avoids leaving a zombie in
	// the brief window before quit.
	_ = cmd.Process.Release()
	return nil
}
