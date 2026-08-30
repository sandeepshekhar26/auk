package importer

import "testing"

// TestDetectAllFormatsRouteCorrectly is the disambiguation guard: every format
// must route to its own importer, and — critically — adding the JSON-based HAR
// and Insomnia sniffers must NOT cause a Postman/OpenAPI input (also JSON) to
// be mis-detected, nor vice versa. Fixtures are the shared consts from each
// format's test file.
func TestDetectAllFormatsRouteCorrectly(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"curl", `curl -X GET https://x.test`, FormatCurl},
		{"openapi", openAPISpec, FormatOpenAPI},
		{"postman", postmanCol, FormatPostman},
		{"har", harSample, FormatHAR},
		{"insomnia", insomniaV4, FormatInsomnia},
		{"bruno", bruSample, FormatBruno},
		{"garbage", `{"random":"object"}`, ""},
	}
	for _, c := range cases {
		if got := Detect(c.content); got != c.want {
			t.Errorf("Detect(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestImportAllFormatsRoute checks the same through the Import entry point,
// which is what app.go calls.
func TestImportAllFormatsRoute(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"openapi", openAPISpec, FormatOpenAPI},
		{"postman", postmanCol, FormatPostman},
		{"har", harSample, FormatHAR},
		{"insomnia", insomniaV4, FormatInsomnia},
		{"bruno", bruSample, FormatBruno},
	}
	for _, c := range cases {
		res, err := Import(c.content)
		if err != nil {
			t.Errorf("Import(%s) error: %v", c.name, err)
			continue
		}
		if res.Format != c.want {
			t.Errorf("Import(%s).Format = %q, want %q", c.name, res.Format, c.want)
		}
		if len(res.Requests) == 0 {
			t.Errorf("Import(%s) produced no requests", c.name)
		}
	}
}
