package templating

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"apitool/internal/core/model"
)

// call invokes a registered template function by name, failing the test on a
// missing registration or an unexpected error.
func call(t *testing.T, e *Engine, name string, args ...string) string {
	t.Helper()
	fn, ok := e.funcs[name]
	if !ok {
		t.Fatalf("function %q is not registered", name)
	}
	out, err := fn(args)
	if err != nil {
		t.Fatalf("%s(%v) unexpected error: %v", name, args, err)
	}
	return out
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRandomUUIDShapes(t *testing.T) {
	e := New(nil)
	for _, name := range []string{"randomUuid", "randomUUID", "guid"} {
		got := call(t, e, name)
		if !uuidRe.MatchString(got) {
			t.Fatalf("%s = %q, not a v4 UUID shape", name, got)
		}
	}
}

func TestRandomIntDefaultAndRange(t *testing.T) {
	e := New(nil)

	// Default range: 0..1000 inclusive.
	for i := 0; i < 200; i++ {
		n, err := strconv.Atoi(call(t, e, "randomInt"))
		if err != nil {
			t.Fatalf("randomInt default not an int: %v", err)
		}
		if n < 0 || n > 1000 {
			t.Fatalf("randomInt default = %d, want within [0,1000]", n)
		}
	}

	// Explicit min,max args are respected.
	for i := 0; i < 200; i++ {
		n, err := strconv.Atoi(call(t, e, "randomInt", "5", "9"))
		if err != nil {
			t.Fatalf("randomInt(5,9) not an int: %v", err)
		}
		if n < 5 || n > 9 {
			t.Fatalf("randomInt(5,9) = %d, want within [5,9]", n)
		}
	}

	// Bad arg counts / non-numeric args error (matching the engine's
	// arg-validation convention used by hash/encode/etc.).
	if _, err := e.funcs["randomInt"]([]string{"1"}); err == nil {
		t.Fatal("randomInt with one arg: expected error, got none")
	}
	if _, err := e.funcs["randomInt"]([]string{"x", "y"}); err == nil {
		t.Fatal("randomInt with non-numeric args: expected error, got none")
	}
}

func TestRandomAlphaNumeric(t *testing.T) {
	e := New(nil)
	alnum := regexp.MustCompile(`^[A-Za-z0-9]+$`)

	// Bare form is a single character (Postman parity).
	if got := call(t, e, "randomAlphaNumeric"); len(got) != 1 || !alnum.MatchString(got) {
		t.Fatalf("randomAlphaNumeric bare = %q, want one alphanumeric char", got)
	}
	// Length argument is honoured.
	if got := call(t, e, "randomAlphaNumeric", "16"); len(got) != 16 || !alnum.MatchString(got) {
		t.Fatalf("randomAlphaNumeric(16) = %q, want 16 alphanumeric chars", got)
	}
	if _, err := e.funcs["randomAlphaNumeric"]([]string{"nope"}); err == nil {
		t.Fatal("randomAlphaNumeric with non-numeric length: expected error, got none")
	}
}

func TestRandomEmailShape(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomEmail")
	at := strings.Index(got, "@")
	if at <= 0 {
		t.Fatalf("randomEmail = %q, missing local@domain", got)
	}
	if !strings.Contains(got[at+1:], ".") {
		t.Fatalf("randomEmail = %q, domain has no dot", got)
	}
}

func TestRandomIpv4Shape(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomIpv4")
	parts := strings.Split(got, ".")
	if len(parts) != 4 {
		t.Fatalf("randomIpv4 = %q, want 4 octets", got)
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			t.Fatalf("randomIpv4 = %q, octet %q out of 0-255", got, p)
		}
	}
	// Postman alias resolves to the same shape.
	if _, ok := e.funcs["randomIP"]; !ok {
		t.Fatal("randomIP (Postman alias) not registered")
	}
}

func TestRandomIpv6Shape(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomIpv6")
	groups := strings.Split(got, ":")
	if len(groups) != 8 {
		t.Fatalf("randomIpv6 = %q, want 8 groups", got)
	}
	hexGroup := regexp.MustCompile(`^[0-9a-f]{4}$`)
	for _, g := range groups {
		if !hexGroup.MatchString(g) {
			t.Fatalf("randomIpv6 = %q, group %q not 4 hex digits", got, g)
		}
	}
}

func TestRandomMacAddressShape(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomMacAddress")
	if !regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`).MatchString(got) {
		t.Fatalf("randomMacAddress = %q, not a MAC address", got)
	}
}

func TestRandomHexColorShape(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomHexColor")
	if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(got) {
		t.Fatalf("randomHexColor = %q, want ^#[0-9a-f]{6}$", got)
	}
}

func TestRandomBoolean(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomBoolean")
	if got != "true" && got != "false" {
		t.Fatalf("randomBoolean = %q, want true/false", got)
	}
}

func TestRandomPrice(t *testing.T) {
	e := New(nil)
	priceRe := regexp.MustCompile(`^\d+\.\d{2}$`)
	if got := call(t, e, "randomPrice"); !priceRe.MatchString(got) {
		t.Fatalf("randomPrice = %q, want a 2-decimal number", got)
	}
	for i := 0; i < 100; i++ {
		got := call(t, e, "randomPrice", "10", "20")
		if !priceRe.MatchString(got) {
			t.Fatalf("randomPrice(10,20) = %q, want a 2-decimal number", got)
		}
		f, _ := strconv.ParseFloat(got, 64)
		if f < 10 || f > 20 {
			t.Fatalf("randomPrice(10,20) = %q, out of range", got)
		}
	}
}

func TestRandomDatesAreRFC3339(t *testing.T) {
	e := New(nil)
	now := time.Now()
	cases := []struct {
		name         string
		past, future bool
	}{
		{"randomDateRecent", true, false},
		{"randomDatePast", true, false},
		{"randomDateFuture", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := call(t, e, tc.name)
			ts, err := time.Parse(time.RFC3339, got)
			if err != nil {
				t.Fatalf("%s = %q, not RFC3339: %v", tc.name, got, err)
			}
			if tc.past && ts.After(now.Add(time.Minute)) {
				t.Fatalf("%s = %q, expected a past date", tc.name, got)
			}
			if tc.future && ts.Before(now.Add(-time.Minute)) {
				t.Fatalf("%s = %q, expected a future date", tc.name, got)
			}
		})
	}
}

func TestRandomLorem(t *testing.T) {
	e := New(nil)
	sentence := call(t, e, "randomLoremSentence")
	if !strings.HasSuffix(sentence, ".") || len(strings.Fields(sentence)) < 4 {
		t.Fatalf("randomLoremSentence = %q, want a period-terminated multi-word sentence", sentence)
	}
	if sentence[0] < 'A' || sentence[0] > 'Z' {
		t.Fatalf("randomLoremSentence = %q, want a capitalised first letter", sentence)
	}
	para := call(t, e, "randomLoremParagraph")
	if strings.Count(para, ".") < 2 {
		t.Fatalf("randomLoremParagraph = %q, want multiple sentences", para)
	}
}

func TestRandomBase64Decodes(t *testing.T) {
	e := New(nil)
	got := call(t, e, "randomBase64")
	if !regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`).MatchString(got) {
		t.Fatalf("randomBase64 = %q, not valid base64", got)
	}
}

// TestRandomValuesDiffer guards the core promise: two evaluations of the same
// random function in one request produce different values. Looped so a rare
// legitimate collision (e.g. randomInt landing on the same value) doesn't
// flake the suite — across many tries at least one pair must differ.
func TestRandomValuesDiffer(t *testing.T) {
	e := New(nil)
	names := []string{
		"randomUuid", "randomInt", "randomAlphaNumeric", "randomEmail", "randomIpv4",
		"randomIpv6", "randomMacAddress", "randomHexColor", "randomPassword", "randomBase64",
		"randomFullName", "randomUrl", "randomDateFuture",
	}
	// randomAlphaNumeric is a single char by default; use a length so its
	// value space is large enough to make a same-value pair astronomically
	// unlikely across the loop.
	argFor := map[string][]string{"randomAlphaNumeric": {"20"}}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			fn := e.funcs[name]
			differed := false
			for i := 0; i < 20; i++ {
				a, err1 := fn(argFor[name])
				b, err2 := fn(argFor[name])
				if err1 != nil || err2 != nil {
					t.Fatalf("%s errored: %v / %v", name, err1, err2)
				}
				if a != b {
					differed = true
					break
				}
			}
			if !differed {
				t.Fatalf("%s produced identical values across 20 pairs", name)
			}
		})
	}
}

// TestBareDynamicVariableResolves is the migration-critical path: an
// argument-less `${randomEmail}` (what an imported Postman `{{$randomEmail}}`
// becomes after the `$random` -> `random` rename) must resolve end-to-end
// through Resolve, without parentheses.
func TestBareDynamicVariableResolves(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{
		WorkspaceID: "ws-1",
		Method:      "POST",
		URL:         "https://api.example.com/u?id=${randomUuid}",
		Body:        &model.RequestBody{Text: `{"email":"${randomEmail}","n":${randomInt}}`},
	}
	resolved, err := e.Resolve(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}
	if strings.Contains(resolved.URL, "${") {
		t.Fatalf("URL still has an unresolved placeholder: %q", resolved.URL)
	}
	if strings.Contains(resolved.Body.Text, "${") {
		t.Fatalf("body still has an unresolved placeholder: %q", resolved.Body.Text)
	}
	if !strings.Contains(resolved.Body.Text, "@") {
		t.Fatalf("resolved body missing an email: %q", resolved.Body.Text)
	}
}

// TestBareEvalAndParenEvalBothWork confirms a dynamic variable resolves both
// bare (${randomInt}) and via the existing call syntax (${randomInt(1,100)}),
// and that a still-undefined bare name remains an error.
func TestBareEvalAndParenEvalBothWork(t *testing.T) {
	e := New(nil)

	if _, err := e.eval(context.Background(), "randomInt", "ws", nil, nil); err != nil {
		t.Fatalf("bare randomInt: unexpected error: %v", err)
	}
	out, err := e.eval(context.Background(), "randomInt(1, 3)", "ws", nil, nil)
	if err != nil {
		t.Fatalf("randomInt(1, 3): unexpected error: %v", err)
	}
	if n, _ := strconv.Atoi(out); n < 1 || n > 3 {
		t.Fatalf("randomInt(1, 3) = %q, want within [1,3]", out)
	}

	if _, err := e.eval(context.Background(), "definitelyNotAFunction", "ws", nil, nil); err == nil {
		t.Fatal("undefined bare name: expected error, got none")
	}
}

// TestUserVariableWinsOverDynamicName confirms a user-defined variable named
// like a dynamic function still takes precedence (variables are checked
// before the function registry in eval).
func TestUserVariableWinsOverDynamicName(t *testing.T) {
	e := New(nil)
	vars := map[string]string{"randomEmail": "fixed@literal.test"}
	got, err := e.eval(context.Background(), "randomEmail", "ws", vars, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fixed@literal.test" {
		t.Fatalf("user variable should win: got %q", got)
	}
}

// TestPostmanBareTimestamps covers the two dynamic variables an imported
// Postman collection reaches by name: `{{$timestamp}}` and
// `{{$isoTimestamp}}` become `${timestamp}` / `${isoTimestamp}` after the
// importer's `$`-stripping rewrite, and used to resolve as NEITHER a variable
// nor a function — so every request carrying `X-Request-Time: {{$timestamp}}`
// failed to send.
func TestPostmanBareTimestamps(t *testing.T) {
	e := New(nil)

	got := call(t, e, "timestamp")
	secs, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("timestamp = %q, want unix seconds: %v", got, err)
	}
	if delta := time.Since(time.Unix(secs, 0)); delta < -time.Minute || delta > time.Minute {
		t.Errorf("timestamp = %d, not within a minute of now", secs)
	}

	iso := call(t, e, "isoTimestamp")
	parsed, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("isoTimestamp = %q, want RFC3339: %v", iso, err)
	}
	if delta := time.Since(parsed); delta < -time.Minute || delta > time.Minute {
		t.Errorf("isoTimestamp = %q, not within a minute of now", iso)
	}
	// Postman's $isoTimestamp is JavaScript's Date.toISOString(): UTC, with
	// millisecond precision and a `Z`.
	if !strings.HasSuffix(iso, "Z") || !strings.Contains(iso, ".") {
		t.Errorf("isoTimestamp = %q, want Postman's millisecond-precision UTC shape", iso)
	}
}

// The bare `${name}` dispatch is what an imported collection actually hits, so
// exercise the whole path for the most-used Postman dynamic variables rather
// than just the function table.
func TestPostmanTopDynamicVariablesResolveBare(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Method: "GET", URL: "https://api.example.com/x",
		Headers: []model.KeyValue{
			{Key: "X-Time", Value: "${timestamp}", Enabled: true},
			{Key: "X-Iso", Value: "${isoTimestamp}", Enabled: true},
			{Key: "X-Guid", Value: "${guid}", Enabled: true},
			{Key: "X-Uuid", Value: "${randomUUID}", Enabled: true},
			{Key: "X-Int", Value: "${randomInt}", Enabled: true},
		},
	}
	resolved, err := e.Resolve(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, h := range resolved.Headers {
		if h.Value == "" || strings.Contains(h.Value, "${") {
			t.Errorf("%s = %q, want a resolved value", h.Key, h.Value)
		}
	}
}

// A user-defined variable named `timestamp` must still win over the new
// function, since eval checks variables first.
func TestUserVariableShadowsBareTimestamp(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Method: "GET", URL: "https://api.example.com/x",
		Headers: []model.KeyValue{{Key: "X-Time", Value: "${timestamp}", Enabled: true}},
	}
	env := &model.Environment{
		ID: "e1", Name: "Env",
		Variables: []model.KeyValue{{Key: "timestamp", Value: "mine", Enabled: true}},
	}
	resolved, err := e.Resolve(context.Background(), req, env, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Headers[0].Value != "mine" {
		t.Errorf("X-Time = %q, want the user's variable to win", resolved.Headers[0].Value)
	}
}
