// Package storage implements core.Store. This file provides fractional
// (LexoRank-style) order keys: reordering or inserting a sibling never
// requires renumbering neighbors, so a reorder is a one-file diff instead of
// a cascade across every sibling's YAML (docs/02-architecture.md §7.1).
//
// The midpoint algorithm is a direct port of the well-known
// fractional-indexing reference implementation (rocicorp/fractional-indexing,
// itself a port of Figma's scheme): digit strings over a fixed alphabet,
// comparable with plain string ordering. Its key invariant is that a
// generated key never ends in the alphabet's lowest digit ('0') — a key
// ending in '0' would make some future "insert directly before it" request
// unsatisfiable (there is no digit lower than '0' to append) — so every
// generated key satisfies this, and OrderKeyBetween defensively trims any
// caller-supplied lower bound that violates it before starting.
package storage

import "strings"

// orderKeyAlphabet is the digit set used for order-key strings, sorted in
// ascending byte order (0-9 < A-Z), which is also plain-string sort order —
// exactly what makes these keys usable as YAML string fields sorted by
// ordinary string comparison.
const orderKeyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

const orderKeyBase = len(orderKeyAlphabet)

// OrderKeyBetween returns a new order key that sorts strictly between a and
// b (plain Go string comparison). Pass "" for a when inserting at the very
// start of a list and "" for b when inserting at the very end. The
// caller-supplied OrderKey on a newly created resource should never be
// trusted as-is for siblings inserted between two others — call this
// instead so concurrent inserts (e.g. from two git branches) pick distinct,
// non-colliding keys.
func OrderKeyBetween(a, b string) string {
	a = strings.TrimRight(a, "0")
	if a != "" && b != "" && a >= b {
		// Defensive: callers should never pass an inverted/equal range.
		return midpoint(a, "")
	}
	return midpoint(a, b)
}

// midpoint returns a string strictly between a and b, assuming a < b as
// plain strings ("" for b means unbounded above). a must not end in '0'.
func midpoint(a, b string) string {
	if b != "" {
		n := commonPrefixLen(a, b)
		if n > 0 {
			return b[:n] + midpoint(a[n:], b[n:])
		}
	}

	digitA := 0
	if a != "" {
		digitA = digitVal(a[0])
	}
	digitB := orderKeyBase
	if b != "" {
		digitB = digitVal(b[0])
	}

	if digitB-digitA > 1 {
		mid := roundHalf(digitA, digitB)
		return string(orderKeyAlphabet[mid])
	}
	if digitA == digitB {
		// a is exhausted (implicit floor 0) and b's leading digit is also
		// 0 (the only way digitA==digitB here, since a=="" forces
		// digitA==0): "0" alone is not a valid final answer (it would
		// equal-prefix-match into b's remainder rather than sort strictly
		// before it, and — more importantly — a key ending in '0' can
		// never have anything inserted before it later). Emit '0' and
		// keep narrowing against whatever follows b's leading digit.
		var bRest string
		if b != "" {
			bRest = b[1:]
		}
		return string(orderKeyAlphabet[0]) + midpoint("", bRest)
	}
	// digitB == digitA+1 here.
	if b != "" && len(b) > 1 && digitB != 0 {
		// b has more digits beyond its first, and its leading digit
		// (non-zero) already sorts strictly after a and strictly before b
		// (b is longer with that same leading digit).
		return b[:1]
	}
	var aRest string
	if a != "" {
		aRest = a[1:]
	}
	return string(orderKeyAlphabet[digitA]) + midpoint(aRest, "")
}

// roundHalf returns round(0.5*(digitA+digitB)) using integer arithmetic
// (banker's rounding is irrelevant here — any value strictly between the
// two digits is correct).
func roundHalf(digitA, digitB int) int {
	return digitA + (digitB-digitA+1)/2
}

func digitVal(c byte) int {
	return strings.IndexByte(orderKeyAlphabet, c)
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
