package storage

import (
	"math/rand"
	"strings"
	"testing"
)

func TestOrderKeyBetween(t *testing.T) {
	tests := []struct {
		name string
		a, b string
	}{
		{"both empty", "", ""},
		{"start of list", "", "M"},
		{"end of list", "M", ""},
		{"adjacent single chars", "A", "B"},
		{"wide gap", "A", "Z"},
		{"identical prefix, adjacent tail", "A0", "A1"},
		{"already deep key vs end", "ZZZZ", ""},
		{"start vs already deep key", "", "000A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrderKeyBetween(tt.a, tt.b)
			if got == "" {
				t.Fatalf("OrderKeyBetween(%q, %q) returned empty string", tt.a, tt.b)
			}
			if tt.a != "" && got <= tt.a {
				t.Errorf("OrderKeyBetween(%q, %q) = %q, want > %q", tt.a, tt.b, got, tt.a)
			}
			if tt.b != "" && got >= tt.b {
				t.Errorf("OrderKeyBetween(%q, %q) = %q, want < %q", tt.a, tt.b, got, tt.b)
			}
		})
	}
}

// TestOrderKeyBetween_RepeatedInsertsStayOrdered simulates repeatedly
// inserting a new sibling immediately after the first element — the
// classic pathological case for naive midpoint schemes (keys should not
// blow up in length unreasonably and must stay strictly ordered). The
// window being narrowed is always [keys[0], keys[1]] where keys[1] is
// itself a previously-generated key, so this never manufactures an
// impossible-to-satisfy hand-written bound.
func TestOrderKeyBetween_RepeatedInsertsStayOrdered(t *testing.T) {
	keys := []string{OrderKeyBetween("", ""), OrderKeyBetween(OrderKeyBetween("", ""), "")}
	for i := 0; i < 100; i++ {
		next := OrderKeyBetween(keys[0], keys[1])
		keys = append([]string{keys[0], next}, keys[1:]...)
		// Check the freshly narrowed pair immediately (this is exactly
		// where a regression surfaced: OrderKeyBetween("I", "I0I") ->
		// "I0", and the very next narrowing OrderKeyBetween("I", "I0")
		// looped back to something >= "I0" since "I0" ends in the
		// alphabet's lowest digit).
		if keys[0] >= keys[1] || keys[1] >= keys[2] {
			t.Fatalf("iteration %d: not strictly ordered: %q >= %q or %q >= %q", i, keys[0], keys[1], keys[1], keys[2])
		}
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys not strictly increasing at index %d: %q >= %q", i, keys[i-1], keys[i])
		}
	}
	if len(keys[len(keys)-1]) > 80 {
		t.Errorf("order key grew unreasonably long: %d chars", len(keys[len(keys)-1]))
	}
}

// TestOrderKeyBetween_NeverEndsInZero asserts the algorithm's key
// invariant (see package doc comment on orderkey.go): a generated key
// never ends in the alphabet's lowest digit, since that would make some
// future "insert directly after it" request unsatisfiable.
func TestOrderKeyBetween_NeverEndsInZero(t *testing.T) {
	last := ""
	for i := 0; i < 100; i++ {
		last = OrderKeyBetween(last, "")
		if strings.HasSuffix(last, "0") {
			t.Fatalf("iteration %d: generated key %q ends in '0'", i, last)
		}
	}
	first := ""
	last = OrderKeyBetween("", "")
	for i := 0; i < 100; i++ {
		next := OrderKeyBetween(first, last)
		if strings.HasSuffix(next, "0") {
			t.Fatalf("iteration %d: generated key %q ends in '0'", i, next)
		}
		last = next
	}
}

func TestOrderKeyBetween_ManySequentialInserts(t *testing.T) {
	// Repeatedly append at the end (the common case: new request added to
	// a folder) and verify strict ordering is maintained.
	last := ""
	var all []string
	for i := 0; i < 200; i++ {
		next := OrderKeyBetween(last, "")
		if last != "" && next <= last {
			t.Fatalf("iteration %d: OrderKeyBetween(%q, \"\") = %q, not > %q", i, last, next, last)
		}
		all = append(all, next)
		last = next
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("not strictly increasing at %d: %q >= %q", i, all[i-1], all[i])
		}
	}
}

// TestOrderKeyBetween_RandomInsertsStayOrdered repeatedly inserts at random
// positions in a growing ordered list (the general case: reordering
// requests/folders in an arbitrary sequence) and checks the whole list
// stays strictly sorted after every insert.
func TestOrderKeyBetween_RandomInsertsStayOrdered(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	keys := []string{OrderKeyBetween("", "")}
	for i := 0; i < 500; i++ {
		idx := rng.Intn(len(keys) + 1)
		var lo, hi string
		if idx > 0 {
			lo = keys[idx-1]
		}
		if idx < len(keys) {
			hi = keys[idx]
		}
		next := OrderKeyBetween(lo, hi)
		if lo != "" && next <= lo {
			t.Fatalf("iteration %d: OrderKeyBetween(%q, %q) = %q, want > %q", i, lo, hi, next, lo)
		}
		if hi != "" && next >= hi {
			t.Fatalf("iteration %d: OrderKeyBetween(%q, %q) = %q, want < %q", i, lo, hi, next, hi)
		}
		keys = append(keys[:idx], append([]string{next}, keys[idx:]...)...)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys not strictly increasing at index %d: %q >= %q", i, keys[i-1], keys[i])
		}
	}
}
