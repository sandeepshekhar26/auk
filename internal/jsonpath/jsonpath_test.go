package jsonpath

import (
	"errors"
	"testing"
)

func TestGet_ObjectField(t *testing.T) {
	got, err := Get(`{"name":"Leanne","age":30}`, "name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "Leanne" {
		t.Fatalf("got %q, want %q", got, "Leanne")
	}
}

func TestGet_NestedFields(t *testing.T) {
	doc := `{"address":{"geo":{"lat":"-37.3159","lng":"81.1496"}}}`
	got, err := Get(doc, "address.geo.lat")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "-37.3159" {
		t.Fatalf("got %q, want %q", got, "-37.3159")
	}
}

func TestGet_ArrayIndex(t *testing.T) {
	doc := `{"items":[{"id":1},{"id":2},{"id":3}]}`
	got, err := Get(doc, "items[1].id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "2" {
		t.Fatalf("got %q, want %q", got, "2")
	}
}

func TestGet_ChainedArrayIndexes(t *testing.T) {
	doc := `{"matrix":[[1,2],[3,4]]}`
	got, err := Get(doc, "matrix[1][0]")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "3" {
		t.Fatalf("got %q, want %q", got, "3")
	}
}

func TestGet_LeadingArrayIndex(t *testing.T) {
	doc := `[{"name":"first"},{"name":"second"}]`
	got, err := Get(doc, "[1].name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

func TestGet_DollarPrefixStripped(t *testing.T) {
	doc := `{"a":{"b":42}}`
	for _, path := range []string{"$.a.b", "$a.b", "a.b"} {
		got, err := Get(doc, path)
		if err != nil {
			t.Fatalf("Get(%q): %v", path, err)
		}
		if got != "42" {
			t.Fatalf("Get(%q) = %q, want %q", path, got, "42")
		}
	}
}

func TestGet_WholeObjectAndArrayRenderCompactJSON(t *testing.T) {
	doc := `{"address":{"city":"Gwenborough","zip":"92998"},"tags":["a","b"]}`

	gotObj, err := Get(doc, "address")
	if err != nil {
		t.Fatalf("Get(address): %v", err)
	}
	if gotObj != `{"city":"Gwenborough","zip":"92998"}` {
		t.Fatalf("got %q", gotObj)
	}

	gotArr, err := Get(doc, "tags")
	if err != nil {
		t.Fatalf("Get(tags): %v", err)
	}
	if gotArr != `["a","b"]` {
		t.Fatalf("got %q", gotArr)
	}
}

func TestGet_ScalarKinds(t *testing.T) {
	doc := `{"s":"hello","n":3.5,"b":true,"nil":null}`
	cases := map[string]string{"s": "hello", "n": "3.5", "b": "true", "nil": ""}
	for path, want := range cases {
		got, err := Get(doc, path)
		if err != nil {
			t.Fatalf("Get(%q): %v", path, err)
		}
		if got != want {
			t.Fatalf("Get(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestGet_FieldNotFound(t *testing.T) {
	_, err := Get(`{"a":1}`, "b")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestGet_IndexOutOfRange(t *testing.T) {
	_, err := Get(`{"items":[1,2]}`, "items[5]")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestGet_IndexIntoNonArray(t *testing.T) {
	_, err := Get(`{"a":1}`, "a[0]")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestGet_FieldIntoNonObject(t *testing.T) {
	_, err := Get(`{"a":1}`, "a.b")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestGet_InvalidJSON(t *testing.T) {
	_, err := Get(`{not json`, "a")
	if err == nil {
		t.Fatal("want error for invalid JSON input")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("invalid JSON should not report as ErrNotFound")
	}
}

func TestGet_EmptyPath(t *testing.T) {
	if _, err := Get(`{"a":1}`, ""); err == nil {
		t.Fatal("want error for empty path")
	}
}

func TestGet_UnterminatedBracket(t *testing.T) {
	if _, err := Get(`{"a":[1]}`, "a[0"); err == nil {
		t.Fatal("want error for unterminated '['")
	}
}

func TestGet_NonIntegerIndex(t *testing.T) {
	if _, err := Get(`{"a":[1]}`, "a[x]"); err == nil {
		t.Fatal("want error for non-integer array index")
	}
}

func TestValueToString(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"nil", nil, ""},
		{"float", 3.0, "3"},
		{"floatFraction", 3.5, "3.5"},
		{"boolTrue", true, "true"},
		{"boolFalse", false, "false"},
		{"object", map[string]any{"a": 1.0}, `{"a":1}`},
		{"array", []any{1.0, 2.0}, `[1,2]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValueToString(tc.in); got != tc.want {
				t.Fatalf("ValueToString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
