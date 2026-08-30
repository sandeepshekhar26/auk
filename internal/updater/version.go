package updater

// Semantic-version parsing and comparison for the auto-updater.
//
// The only versions this ever compares are:
//   - the CURRENT running app's version, read from the bundle's
//     CFBundleShortVersionString (current.go), and
//   - the LATEST release's tag, read from the GitHub releases feed (feed.go).
//
// Both are plain "MAJOR.MINOR.PATCH" with an optional "-prerelease" and an
// optional "+build" suffix (SemVer 2.0.0, the subset we actually emit). The
// comparison follows SemVer precedence: build metadata is ignored, and a
// pre-release ("0.4.0-dev.3") sorts BEFORE its associated normal release
// ("0.4.0"). That last rule is the whole point of doing this properly rather
// than with strings.Compare — it is what guarantees a local dev build that
// derives from an upcoming 0.4.0 is never told that 0.4.0 (or an older 0.3.0)
// is "newer" than it.

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed SemVer. Build metadata is intentionally discarded on
// parse — it never affects precedence.
type Version struct {
	Major, Minor, Patch int
	// Pre holds the dot-separated pre-release identifiers ("dev", "3"), empty
	// for a normal release. Its presence lowers precedence vs the same core.
	Pre []string
}

// ParseVersion parses "v0.3.0", "0.3.0", "0.4.0-dev.3", "1.2.3+abc" etc. A
// leading "v" is tolerated (GitHub tags carry it; the DMG name does not). An
// empty or malformed string is an error — callers treat that as "dev/unknown"
// (see IsDevVersion) rather than crashing.
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(raw, "v")
	raw = strings.TrimPrefix(raw, "V")
	if raw == "" {
		return Version{}, fmt.Errorf("empty version string")
	}

	// Split off build metadata (+...) first — it plays no part in precedence.
	if i := strings.IndexByte(raw, '+'); i >= 0 {
		raw = raw[:i]
	}

	// Split off the pre-release (-...).
	var pre []string
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		preStr := raw[i+1:]
		raw = raw[:i]
		if preStr == "" {
			return Version{}, fmt.Errorf("empty pre-release in %q", s)
		}
		pre = strings.Split(preStr, ".")
		for _, id := range pre {
			if id == "" {
				return Version{}, fmt.Errorf("empty pre-release identifier in %q", s)
			}
		}
	}

	core := strings.Split(raw, ".")
	if len(core) != 3 {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	nums := make([]int, 3)
	for i, part := range core {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q has a non-numeric component %q", s, part)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// Compare returns -1 if a < b, 0 if a == b, +1 if a > b, per SemVer precedence.
func Compare(a, b Version) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	// Cores are equal. A version WITH a pre-release is lower than one without.
	aPre, bPre := len(a.Pre) > 0, len(b.Pre) > 0
	switch {
	case !aPre && !bPre:
		return 0
	case aPre && !bPre:
		return -1 // a is a pre-release of b's core → a is older
	case !aPre && bPre:
		return 1
	}
	return comparePre(a.Pre, b.Pre)
}

// comparePre compares two non-empty pre-release identifier lists per SemVer:
// numeric identifiers compare numerically, alphanumeric lexically; numeric
// ranks below alphanumeric; and when one list is a prefix of the other the
// longer one wins.
func comparePre(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		ai, aNum := toNum(a[i])
		bi, bNum := toNum(b[i])
		switch {
		case aNum && bNum:
			if c := cmpInt(ai, bi); c != 0 {
				return c
			}
		case aNum && !bNum:
			return -1 // numeric identifiers have lower precedence
		case !aNum && bNum:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func toNum(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// IsDevVersion reports whether s looks like an unversioned local/dev build
// that must never be nagged. That is: an empty/whitespace string, anything
// that does not parse as SemVer, or a 0.0.0 core (release.sh's "0.0.0-dev"
// fallback when no tag/AUK_VERSION is set). A `wails dev` bundle instead
// reports Wails' unset-productVersion default of "1.0.0", which parses fine
// and simply compares as newer than any real release — also never nagged, but
// via the comparison rather than this predicate.
func IsDevVersion(s string) bool {
	v, err := ParseVersion(s)
	if err != nil {
		return true
	}
	return v.Major == 0 && v.Minor == 0 && v.Patch == 0
}

// IsNewer reports whether latest is strictly newer than current. A current
// version that IsDevVersion is never considered older than anything (dev
// builds are never nagged), and any unparseable latest is treated as "not
// newer" (a malformed feed must not trigger an update).
func IsNewer(latest, current string) bool {
	if IsDevVersion(current) {
		return false
	}
	lv, err := ParseVersion(latest)
	if err != nil {
		return false
	}
	cv, err := ParseVersion(current)
	if err != nil {
		return false
	}
	return Compare(lv, cv) > 0
}
