// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package bigprec bounds a pxf.BigFloat's precision without computing
// anything that precision would size. It is shared by encoding/pxf, which
// bounds a literal's precision suffix and the literals it writes, and
// encoding/pb, which bounds a prec read off the wire (draft -01 § Mandatory
// Limits, trendvidia/protowire#406 and #499).
package bigprec

import "math/bits"

// Digits is N, the number of significant digits a BigFloat of prec bits is
// written with (draft -01 § PXF Annotations, protowire#406): the decimal
// digits of 2^prec, plus one, the fewest for which every prec-bit value is
// recovered from its literal. One fewer lost 9,157 of 20,000 random 53-bit
// values (measured on #406). ok reports whether N is within limit, which
// is MaxNumericLiteralDigits: prec 13603 at the default is the last that
// is. prec comes off the wire or out of a document, so 2^prec is never
// computed (the draft says so of a decoder): see Pow2Digits.
func Digits(prec uint32, limit int) (n int, ok bool) {
	n = Pow2Digits(prec) + 1
	return n, n <= limit
}

// log10Of2Hi and log10Of2Lo are floor(log10(2) × 2^128), high word first.
const log10Of2Hi, log10Of2Lo = 0x4d104d427de7fbcc, 0x47c4acd605be48bc

// Pow2Digits is the number of decimal digits of 2^p, floor(p·log10(2))
// + 1, without computing 2^p. The product is taken in 128-bit fixed
// point: it falls short of p·log10(2) by less than 2^-95, and for every
// uint32 p that product is further than that from an integer, so the
// floor is exact. float64 is not enough: it is one digit high at p =
// 146964308, the first of several below 2^32 (TestPow2Digits).
func Pow2Digits(p uint32) int {
	hi, lo := bits.Mul64(uint64(p), log10Of2Hi)
	carryIn, _ := bits.Mul64(uint64(p), log10Of2Lo)
	_, carry := bits.Add64(lo, carryIn, 0)
	return int(hi+carry) + 1
}
