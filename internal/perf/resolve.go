// Package perf runs k6 load tests against a saved request. k6 is invoked
// STRICTLY as an arm's-length CLI child process — never imported as a Go
// library, never go:embed'd into the binary, never xk6-compiled — because
// k6 is AGPLv3 and any of those would relicense this entire app (see
// docs/02-architecture.md §11 and docs/04-architecture-critique.md). This
// package only ever exec's a separately-distributed k6 binary.
package perf

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// EnvK6Bin lets a developer point at any k6 binary (e.g. build/sidecars/k6)
// without a full app bundle. Takes precedence over all other resolution.
const EnvK6Bin = "APITOOL_K6_BIN"

// ResolveK6 locates the bundled k6 binary. Order:
//  1. $APITOOL_K6_BIN (explicit override, used in dev and tests)
//  2. the app bundle's Resources dir (macOS: <App>.app/Contents/Resources/k6)
//     or next to the executable (Windows/Linux)
//  3. a repo-relative build/sidecars/k6 (running `wails dev` / `go run`)
//  4. k6 on $PATH (last resort; may be a user-installed k6)
//
// It returns a clear, actionable error if none is found, since "run a load
// test" is dead without it.
func ResolveK6() (string, error) {
	if p := os.Getenv(EnvK6Bin); p != "" {
		if isExecutable(p) {
			return p, nil
		}
		return "", fmt.Errorf("%s=%q is not an executable file", EnvK6Bin, p)
	}

	name := "k6"
	if runtime.GOOS == "windows" {
		name = "k6.exe"
	}

	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(dir, name),                    // beside the binary
			filepath.Join(dir, "..", "Resources", name), // macOS .app bundle
			filepath.Join(dir, "..", "Resources", "bin", name),
		}
		for _, c := range candidates {
			if isExecutable(c) {
				return filepath.Clean(c), nil
			}
		}
	}

	if repo := repoSidecar(name); repo != "" {
		return repo, nil
	}

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("k6 binary not found: set %s, bundle it in Resources/, place it in build/sidecars/, or install k6 on PATH", EnvK6Bin)
}

// repoSidecar walks up from the cwd looking for build/sidecars/<name>, so
// `go run`/`wails dev`/`go test` find the dev k6 without any env setup.
func repoSidecar(name string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 8; i++ {
		c := filepath.Join(dir, "build", "sidecars", name)
		if isExecutable(c) {
			return c
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return false
	}
	// On Windows the executable bit isn't meaningful; existence is enough.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}
