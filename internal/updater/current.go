package updater

// Resolving the CURRENT running app's version.
//
// A released AUK has its version stamped into the bundle at build time:
// scripts/release.sh writes wails.json's info.productVersion, which Wails
// renders into Contents/Info.plist as CFBundleShortVersionString (see
// build/darwin/Info.plist's {{.Info.ProductVersion}} template). So the
// authoritative source of the running version is that plist, located relative
// to the running executable — NOT a compiled-in constant, which would drift
// from what release.sh actually stamped.
//
// Resolution order:
//  1. Contents/Info.plist CFBundleShortVersionString (a real, installed .app).
//  2. buildVersion, an -ldflags override (empty by default) for a `go build`
//     of the raw binary that still wants to advertise a version.
//  3. AUK_VERSION env — the dev/test override, and the same variable
//     release.sh reads, so a tester can force any version without rebuilding.
//  4. "" — dev/unknown. IsDevVersion("") is true, so nothing gets nagged.
//
// Note a plain `wails dev` run lands on step 1 with Wails' unset-productVersion
// default of "1.0.0" (internal/project.Info: ProductVersion defaults to
// "1.0.0"), which is why dev never sees a spurious update — 1.0.0 outranks
// every real release. The frontend additionally skips the check entirely under
// import.meta.env.DEV.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// buildVersion can be set at link time:
//
//	go build -ldflags "-X apitool/internal/updater.buildVersion=0.3.0"
//
// It is only a fallback for a non-bundle binary; a real release relies on the
// plist (step 1) and leaves this empty.
var buildVersion string

// shortVersionRE pulls the string value that immediately follows the
// CFBundleShortVersionString key in an XML plist. Wails writes the bundle
// plist as XML (it does not run plutil to binary-encode it), and codesign /
// notarization do not rewrite it, so a text match is sufficient and keeps this
// dependency-free and unit-testable. A binary plist (should one ever appear)
// simply won't match and we fall through to the dev sentinel.
var shortVersionRE = regexp.MustCompile(`(?s)<key>\s*CFBundleShortVersionString\s*</key>\s*<string>([^<]*)</string>`)

// CurrentVersion resolves the running app's version per the order documented
// above. An empty return means "dev/unknown".
func CurrentVersion() string {
	if v := versionFromBundle(); v != "" {
		return v
	}
	if v := strings.TrimSpace(buildVersion); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("AUK_VERSION")); v != "" {
		return v
	}
	return ""
}

// versionFromBundle finds the .app's Contents/Info.plist relative to the
// running executable and returns its CFBundleShortVersionString, or "" if
// there is no bundle (a bare `go build` binary) or no usable value.
func versionFromBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// Resolve symlinks so a /usr/local/bin symlink into a bundle still finds
	// the plist.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	plist := infoPlistPath(exe)
	if plist == "" {
		return ""
	}
	data, err := os.ReadFile(plist)
	if err != nil {
		return ""
	}
	return parseShortVersion(string(data))
}

// infoPlistPath returns the bundle's Contents/Info.plist for an executable at
// .../Foo.app/Contents/MacOS/Foo, or "" when exe is not inside a .app bundle.
// It walks up looking for the "Contents" dir whose parent ends in ".app",
// which is more robust than assuming a fixed depth.
func infoPlistPath(exe string) string {
	dir := filepath.Dir(exe) // .../Contents/MacOS
	for i := 0; i < 6 && dir != "" && dir != string(filepath.Separator); i++ {
		if filepath.Base(dir) == "Contents" && strings.HasSuffix(filepath.Base(filepath.Dir(dir)), ".app") {
			return filepath.Join(dir, "Info.plist")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// parseShortVersion extracts CFBundleShortVersionString from XML plist text.
// A value that is empty or still contains the unrendered "{{" template marker
// (i.e. a plist that was never processed) is treated as absent.
func parseShortVersion(plistXML string) string {
	m := shortVersionRE.FindStringSubmatch(plistXML)
	if m == nil {
		return ""
	}
	v := strings.TrimSpace(m[1])
	if v == "" || strings.Contains(v, "{{") {
		return ""
	}
	return v
}
