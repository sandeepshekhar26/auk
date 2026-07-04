package model

import "testing"

func TestReasonPhrase(t *testing.T) {
	cases := map[string]string{
		"200 OK":                    "OK",
		"404 Not Found":             "Not Found",
		"500 Internal Server Error": "Internal Server Error",
		"201 ":                      "", // reason present but empty
		"418 I'm a teapot":          "I'm a teapot",
		"200":                       "200", // no space: returned as-is (never emitted by net/http)
		"":                          "",
	}
	for in, want := range cases {
		if got := ReasonPhrase(in); got != want {
			t.Errorf("ReasonPhrase(%q) = %q, want %q", in, got, want)
		}
	}
}
