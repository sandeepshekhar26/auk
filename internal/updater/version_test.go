package updater

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in                        string
		wantMaj, wantMin, wantPat int
		wantPre                   []string
		wantErr                   bool
	}{
		{in: "0.3.0", wantMaj: 0, wantMin: 3, wantPat: 0},
		{in: "v0.3.0", wantMaj: 0, wantMin: 3, wantPat: 0},
		{in: "  v1.2.3 ", wantMaj: 1, wantMin: 2, wantPat: 3},
		{in: "1.2.3+build.9", wantMaj: 1, wantMin: 2, wantPat: 3},
		{in: "0.4.0-dev.3", wantMaj: 0, wantMin: 4, wantPat: 0, wantPre: []string{"dev", "3"}},
		{in: "0.4.0-rc.1+abc", wantMaj: 0, wantMin: 4, wantPat: 0, wantPre: []string{"rc", "1"}},
		{in: "", wantErr: true},
		{in: "1.2", wantErr: true},
		{in: "1.2.x", wantErr: true},
		{in: "1.2.3-", wantErr: true},
		{in: "{{.Info.ProductVersion}}", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseVersion(%q): expected error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVersion(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got.Major != c.wantMaj || got.Minor != c.wantMin || got.Patch != c.wantPat {
			t.Errorf("ParseVersion(%q) core = %d.%d.%d, want %d.%d.%d", c.in, got.Major, got.Minor, got.Patch, c.wantMaj, c.wantMin, c.wantPat)
		}
		if len(got.Pre) != len(c.wantPre) {
			t.Errorf("ParseVersion(%q) pre = %v, want %v", c.in, got.Pre, c.wantPre)
			continue
		}
		for i := range c.wantPre {
			if got.Pre[i] != c.wantPre[i] {
				t.Errorf("ParseVersion(%q) pre[%d] = %q, want %q", c.in, i, got.Pre[i], c.wantPre[i])
			}
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.3.0", "0.4.0", -1},
		{"0.4.0", "0.3.0", 1},
		{"0.3.0", "0.3.0", 0},
		{"1.0.0", "0.3.0", 1}, // wails-dev default vs real release
		{"0.3.1", "0.3.0", 1},
		{"0.10.0", "0.9.0", 1}, // numeric, not lexical
		// Pre-release sorts BEFORE the associated release — a dev build is never
		// "newer" than the release it derives from.
		{"0.4.0-dev.3", "0.4.0", -1},
		{"0.4.0", "0.4.0-dev.3", 1},
		{"0.4.0-dev.2", "0.4.0-dev.3", -1},
		{"0.4.0-dev.3", "0.4.0-dev.3", 0},
		{"0.4.0-alpha", "0.4.0-alpha.1", -1}, // fewer identifiers < more
		{"0.4.0-alpha.1", "0.4.0-beta", -1},  // lexical among alphanumerics
		{"0.4.0-1", "0.4.0-alpha", -1},       // numeric < alphanumeric
		// Build metadata is ignored in precedence.
		{"1.2.3+a", "1.2.3+b", 0},
	}
	for _, c := range cases {
		av, err := ParseVersion(c.a)
		if err != nil {
			t.Fatalf("parse %q: %v", c.a, err)
		}
		bv, err := ParseVersion(c.b)
		if err != nil {
			t.Fatalf("parse %q: %v", c.b, err)
		}
		if got := Compare(av, bv); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDevVersion(t *testing.T) {
	dev := []string{"", "   ", "0.0.0", "0.0.0-dev", "not-a-version", "{{.Info.ProductVersion}}"}
	for _, v := range dev {
		if !IsDevVersion(v) {
			t.Errorf("IsDevVersion(%q) = false, want true", v)
		}
	}
	real := []string{"0.3.0", "1.0.0", "0.4.0-rc.1", "v2.1.0"}
	for _, v := range real {
		if IsDevVersion(v) {
			t.Errorf("IsDevVersion(%q) = true, want false", v)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.4.0", "0.3.0", true},       // real update
		{"0.3.0", "0.3.0", false},      // same
		{"0.3.0", "0.4.0", false},      // older release than installed
		{"0.3.0", "1.0.0", false},      // wails-dev default (1.0.0) is never "behind"
		{"0.4.0", "", false},           // dev/unknown current is never nagged
		{"0.4.0", "0.0.0-dev", false},  // 0.0.0 dev build is never nagged
		{"0.4.0", "0.4.0-dev.3", true}, // release supersedes its own pre-release
		{"garbage", "0.3.0", false},    // malformed feed must not trigger update
	}
	for _, c := range cases {
		if got := IsNewer(c.latest, c.current); got != c.want {
			t.Errorf("IsNewer(latest=%q, current=%q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
