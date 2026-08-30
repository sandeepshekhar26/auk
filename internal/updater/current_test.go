package updater

import (
	"path/filepath"
	"testing"
)

const sampleInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>AUK</string>
	<key>CFBundleShortVersionString</key>
	<string>0.3.0</string>
	<key>CFBundleVersion</key>
	<string>0.3.0</string>
</dict>
</plist>`

func TestParseShortVersion(t *testing.T) {
	if got := parseShortVersion(sampleInfoPlist); got != "0.3.0" {
		t.Errorf("parseShortVersion = %q, want 0.3.0", got)
	}
	// An unrendered template (dev, productVersion unset before Wails defaults
	// it) or an empty value must read as absent → "".
	empty := `<dict><key>CFBundleShortVersionString</key><string></string></dict>`
	if got := parseShortVersion(empty); got != "" {
		t.Errorf("parseShortVersion(empty) = %q, want empty", got)
	}
	tmpl := `<dict><key>CFBundleShortVersionString</key><string>{{.Info.ProductVersion}}</string></dict>`
	if got := parseShortVersion(tmpl); got != "" {
		t.Errorf("parseShortVersion(template) = %q, want empty", got)
	}
	if got := parseShortVersion("<dict></dict>"); got != "" {
		t.Errorf("parseShortVersion(missing) = %q, want empty", got)
	}
}

func TestInfoPlistPath(t *testing.T) {
	// Typical macOS layout.
	exe := filepath.Join("/Applications", "AUK.app", "Contents", "MacOS", "AUK")
	want := filepath.Join("/Applications", "AUK.app", "Contents", "Info.plist")
	if got := infoPlistPath(exe); got != want {
		t.Errorf("infoPlistPath(%q) = %q, want %q", exe, got, want)
	}
	// Not inside a bundle → "".
	if got := infoPlistPath("/usr/local/bin/auk"); got != "" {
		t.Errorf("infoPlistPath(non-bundle) = %q, want empty", got)
	}
}

func TestAppBundleRoot(t *testing.T) {
	exe := filepath.Join("/Applications", "AUK.app", "Contents", "MacOS", "AUK")
	want := filepath.Join("/Applications", "AUK.app")
	if got := appBundleRoot(exe); got != want {
		t.Errorf("appBundleRoot(%q) = %q, want %q", exe, got, want)
	}
	if got := appBundleRoot("/usr/local/bin/auk"); got != "" {
		t.Errorf("appBundleRoot(non-bundle) = %q, want empty", got)
	}
}
