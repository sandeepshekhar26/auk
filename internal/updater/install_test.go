package updater

import (
	"context"
	"fmt"
	"testing"
)

// fakeRunner drives verifyAppBundle without touching codesign/spctl/hdiutil.
type fakeRunner struct {
	fn func(name string, args []string) (stdout, stderr string, err error)
}

func (f fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	o, e, err := f.fn(name, args)
	return []byte(o), []byte(e), err
}

func TestParseTeamID(t *testing.T) {
	out := `Executable=/Volumes/AUK/AUK.app/Contents/MacOS/AUK
Identifier=com.wails.AUK
Format=app bundle with Mach-O thin (arm64)
TeamIdentifier=V8SAC4GCQQ
Sealed Resources version=2`
	if got := parseTeamID(out); got != "V8SAC4GCQQ" {
		t.Errorf("parseTeamID = %q, want V8SAC4GCQQ", got)
	}
	if got := parseTeamID("TeamIdentifier=not set"); got != "" {
		t.Errorf("parseTeamID(not set) = %q, want empty", got)
	}
	if got := parseTeamID("no team here"); got != "" {
		t.Errorf("parseTeamID(absent) = %q, want empty", got)
	}
}

func TestNotarizationAccepted(t *testing.T) {
	accepted := "/Volumes/AUK/AUK.app: accepted\nsource=Notarized Developer ID\norigin=Developer ID Application: Sandeep Kumar (V8SAC4GCQQ)"
	if !notarizationAccepted(accepted) {
		t.Error("notarizationAccepted = false for an accepted notarized app")
	}
	// Signed but NOT notarized must not pass.
	devIDOnly := "/Volumes/AUK/AUK.app: accepted\nsource=Developer ID"
	if notarizationAccepted(devIDOnly) {
		t.Error("notarizationAccepted = true for a non-notarized Developer ID app")
	}
	rejected := "/Volumes/AUK/AUK.app: rejected\nsource=no usable signature"
	if notarizationAccepted(rejected) {
		t.Error("notarizationAccepted = true for a rejected app")
	}
}

func TestParseMountPoint(t *testing.T) {
	out := "/dev/disk4          \tGUID_partition_scheme          \t\n/dev/disk4s1        \tApple_HFS                      \t/Volumes/AUK 0.3.0"
	if got := parseMountPoint(out); got != "/Volumes/AUK 0.3.0" {
		t.Errorf("parseMountPoint = %q, want /Volumes/AUK 0.3.0", got)
	}
	if got := parseMountPoint("nothing mounted"); got != "" {
		t.Errorf("parseMountPoint(none) = %q, want empty", got)
	}
}

func TestFindAppInEntries(t *testing.T) {
	// The drag-target "Applications" symlink must be ignored in favour of the app.
	names := []string{".background", "Applications", "AUK.app", ".fseventsd"}
	if got := findAppInEntries(names); got != "AUK.app" {
		t.Errorf("findAppInEntries = %q, want AUK.app", got)
	}
	if got := findAppInEntries([]string{"README.txt"}); got != "" {
		t.Errorf("findAppInEntries(none) = %q, want empty", got)
	}
}

// verifyOK is a fakeRunner where every check passes with our Team ID.
func verifyOK() fakeRunner {
	return fakeRunner{fn: func(name string, args []string) (string, string, error) {
		switch {
		case name == "codesign" && hasArg(args, "--verify"):
			return "", "valid on disk", nil
		case name == "codesign" && hasArg(args, "-dvvv"):
			return "", "Identifier=com.wails.AUK\nTeamIdentifier=V8SAC4GCQQ\n", nil
		case name == "spctl":
			return "", "/x/AUK.app: accepted\nsource=Notarized Developer ID\n", nil
		}
		return "", "", fmt.Errorf("unexpected command %s %v", name, args)
	}}
}

func TestVerifyAppBundle_Accept(t *testing.T) {
	if err := verifyAppBundle(context.Background(), verifyOK(), "/x/AUK.app", DefaultTeamID); err != nil {
		t.Errorf("verifyAppBundle should accept a valid, notarized, correctly-signed app: %v", err)
	}
}

func TestVerifyAppBundle_RejectsBadSignature(t *testing.T) {
	r := fakeRunner{fn: func(name string, args []string) (string, string, error) {
		if name == "codesign" && hasArg(args, "--verify") {
			return "", "code object is not signed at all", fmt.Errorf("exit 1")
		}
		return "", "", nil
	}}
	if err := verifyAppBundle(context.Background(), r, "/x/AUK.app", DefaultTeamID); err == nil {
		t.Error("verifyAppBundle must reject a bundle whose signature does not verify")
	}
}

func TestVerifyAppBundle_RejectsWrongTeamID(t *testing.T) {
	r := fakeRunner{fn: func(name string, args []string) (string, string, error) {
		switch {
		case name == "codesign" && hasArg(args, "--verify"):
			return "", "valid on disk", nil
		case name == "codesign" && hasArg(args, "-dvvv"):
			// Correctly signed & notarizable — but by SOMEONE ELSE. This is the
			// attack the Team-ID check exists to stop.
			return "", "TeamIdentifier=ABCDE12345\n", nil
		case name == "spctl":
			return "", "accepted\nsource=Notarized Developer ID\n", nil
		}
		return "", "", nil
	}}
	err := verifyAppBundle(context.Background(), r, "/x/AUK.app", DefaultTeamID)
	if err == nil {
		t.Fatal("verifyAppBundle must reject an app signed by a different Team ID, even if notarized")
	}
}

func TestVerifyAppBundle_RejectsNotNotarized(t *testing.T) {
	r := fakeRunner{fn: func(name string, args []string) (string, string, error) {
		switch {
		case name == "codesign" && hasArg(args, "--verify"):
			return "", "valid on disk", nil
		case name == "codesign" && hasArg(args, "-dvvv"):
			return "", "TeamIdentifier=V8SAC4GCQQ\n", nil
		case name == "spctl":
			// Gatekeeper rejects: not notarized.
			return "", "/x/AUK.app: rejected\nsource=no usable signature", fmt.Errorf("exit 3")
		}
		return "", "", nil
	}}
	if err := verifyAppBundle(context.Background(), r, "/x/AUK.app", DefaultTeamID); err == nil {
		t.Error("verifyAppBundle must reject an app Gatekeeper does not accept")
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
