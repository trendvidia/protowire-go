// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/internal/bigprec"
)

// Wrapper type full names mapped to inner value kind.
var wrapperTypes = map[protoreflect.FullName]protoreflect.Kind{
	"google.protobuf.BoolValue":   protoreflect.BoolKind,
	"google.protobuf.BytesValue":  protoreflect.BytesKind,
	"google.protobuf.DoubleValue": protoreflect.DoubleKind,
	"google.protobuf.FloatValue":  protoreflect.FloatKind,
	"google.protobuf.Int32Value":  protoreflect.Int32Kind,
	"google.protobuf.Int64Value":  protoreflect.Int64Kind,
	"google.protobuf.StringValue": protoreflect.StringKind,
	"google.protobuf.UInt32Value": protoreflect.Uint32Kind,
	"google.protobuf.UInt64Value": protoreflect.Uint64Kind,
}

func isWrapperType(desc protoreflect.MessageDescriptor) bool {
	_, ok := wrapperTypes[desc.FullName()]
	return ok && hasKnownShape(desc)
}

// wktField is one field a codec reads on a well-known type.
type wktField struct {
	name     protoreflect.Name
	kind     protoreflect.Kind
	repeated bool
	optional bool // may be undeclared; when declared it must still match
}

// wktShapes is draft -01 §wkt-shapes: the fields a message must declare
// to be treated as the well-known type its full name says it is. Field
// numbers are not in it, since PXF binds fields by name. The canonical
// definition of each type has this shape, and any copy of it.
var wktShapes = func() map[protoreflect.FullName][]wktField {
	seconds := []wktField{{name: "seconds", kind: protoreflect.Int64Kind}, {name: "nanos", kind: protoreflect.Int32Kind}}
	m := map[protoreflect.FullName][]wktField{
		"google.protobuf.Timestamp": seconds,
		"google.protobuf.Duration":  seconds,
		"google.protobuf.Any": {
			{name: "type_url", kind: protoreflect.StringKind},
			{name: "value", kind: protoreflect.BytesKind},
		},
		"google.protobuf.FieldMask": {{name: "paths", kind: protoreflect.StringKind, repeated: true}},
		"pxf.BigInt": {
			{name: "abs", kind: protoreflect.BytesKind},
			{name: "negative", kind: protoreflect.BoolKind},
		},
		"pxf.Decimal": {
			{name: "unscaled", kind: protoreflect.BytesKind},
			{name: "scale", kind: protoreflect.Int32Kind},
			{name: "negative", kind: protoreflect.BoolKind},
		},
		"pxf.BigFloat": {
			{name: "mantissa", kind: protoreflect.BytesKind},
			{name: "exponent", kind: protoreflect.Int32Kind},
			{name: "prec", kind: protoreflect.Uint32Kind},
			{name: "negative", kind: protoreflect.BoolKind},
		},
		"pxf.Secret": {
			{name: "value", kind: protoreflect.StringKind},
			{name: "hint", kind: protoreflect.StringKind, optional: true},
			{name: "fingerprint", kind: protoreflect.StringKind, optional: true},
		},
	}
	for name, kind := range wrapperTypes {
		m[name] = []wktField{{name: "value", kind: kind}}
	}
	return m
}()

// wktShapeProblem says what stops desc having the shape its full name
// needs, or returns "" when it has it or the name needs none.
func wktShapeProblem(desc protoreflect.MessageDescriptor) string {
	for _, f := range wktShapes[desc.FullName()] {
		fd := desc.Fields().ByName(f.name)
		switch {
		case fd == nil && f.optional:
		case fd == nil:
			return fmt.Sprintf("it has no field %q", f.name)
		case fd.Kind() != f.kind:
			return fmt.Sprintf("field %q is %s, and must be %s", f.name, fd.Kind(), f.kind)
		case fd.IsMap() || fd.IsList() != f.repeated:
			return fmt.Sprintf("field %q has the wrong cardinality", f.name)
		}
	}
	return ""
}

// wktShapeCache memoizes hasKnownShape per descriptor, bounded like the
// bind-time validation caches. The check runs for every value of a
// well-known type, and walking the fields cost 13% on a document of
// nothing else.
var (
	wktShapeCache sync.Map // protoreflect.MessageDescriptor -> bool
	wktShapeCount atomic.Int64
)

// hasKnownShape reports whether desc, whose name is already known to be
// a well-known type's, can be handled as that type. One that cannot is
// an ordinary message (draft -01 §wkt-shapes): bind-time validation
// refuses it, and this is what holds on the paths that do not validate,
// Marshal and SkipValidate among them.
func hasKnownShape(desc protoreflect.MessageDescriptor) bool {
	// A sync.Map panics on an unhashable key; see loadCached.
	cacheable := reflect.TypeOf(desc).Comparable()
	if cacheable {
		if v, hit := wktShapeCache.Load(desc); hit {
			return v.(bool)
		}
	}
	ok := wktShapeProblem(desc) == ""
	if cacheable && wktShapeCount.Load() < maxCachedDescriptors {
		if _, loaded := wktShapeCache.LoadOrStore(desc, ok); !loaded {
			wktShapeCount.Add(1)
		}
	}
	return ok
}

func isTimestamp(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "google.protobuf.Timestamp" && hasKnownShape(desc)
}

func isDuration(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "google.protobuf.Duration" && hasKnownShape(desc)
}

func isAny(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "google.protobuf.Any" && hasKnownShape(desc)
}

func setTimestampFields(msg protoreflect.Message, t time.Time) {
	d := msg.Descriptor()
	msg.Set(d.Fields().ByName("seconds"), protoreflect.ValueOfInt64(t.Unix()))
	msg.Set(d.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(t.Nanosecond())))
}

func setDurationFields(msg protoreflect.Message, dur time.Duration) {
	d := msg.Descriptor()
	nanos := dur.Nanoseconds()
	secs := nanos / 1e9
	nanos -= secs * 1e9
	msg.Set(d.Fields().ByName("seconds"), protoreflect.ValueOfInt64(secs))
	msg.Set(d.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(nanos)))
}

func readTimestamp(msg protoreflect.Message) time.Time {
	d := msg.Descriptor()
	secs := msg.Get(d.Fields().ByName("seconds")).Int()
	nanos := msg.Get(d.Fields().ByName("nanos")).Int()
	return time.Unix(secs, nanos).UTC()
}

func readDuration(msg protoreflect.Message) time.Duration {
	d := msg.Descriptor()
	secs := msg.Get(d.Fields().ByName("seconds")).Int()
	nanos := msg.Get(d.Fields().ByName("nanos")).Int()
	return time.Duration(secs)*time.Second + time.Duration(nanos)*time.Nanosecond
}

// durationFitsLiteral reports whether msg, a google.protobuf.Duration, can
// be written as a literal. A literal holds 64 bits of nanoseconds, about
// 292 years, while the type reaches about 10,000 years; a longer value is
// written as a block, and readDuration must not see it, because it wraps.
func durationFitsLiteral(msg protoreflect.Message) bool {
	d := msg.Descriptor()
	secs := msg.Get(d.Fields().ByName("seconds")).Int()
	nanos := msg.Get(d.Fields().ByName("nanos")).Int()
	// Well inside the limit whatever nanos holds; int32 is about 2.1s.
	if secs > -9_223_372_030 && secs < 9_223_372_030 {
		return true
	}
	total := new(big.Int).Mul(big.NewInt(secs), big.NewInt(int64(time.Second)))
	return total.Add(total, big.NewInt(nanos)).IsInt64()
}

// Big number type detection.

func isBigInt(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "pxf.BigInt" && hasKnownShape(desc)
}

func isDecimal(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "pxf.Decimal" && hasKnownShape(desc)
}

func isBigFloat(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "pxf.BigFloat" && hasKnownShape(desc)
}

// defaultableMessage reports whether a (pxf.default) literal can denote
// desc — the message types draft -01 §annotation-extensions ("Default
// Placement") admits, which is exactly the set applyMessageDefault
// dispatches on before it gives up. [checkDefaultOption] rejects every
// other message type at bind time, so the two must move together: adding
// a branch to applyMessageDefault means adding it here.
//
// Note this is narrower than "well-known type" as the draft's terminology
// section defines it. google.protobuf.Any, FieldMask and Struct are
// well-known types with no single-literal PXF form, and pxf.Secret's two
// surface forms are block-shaped rather than literal-shaped.
func defaultableMessage(desc protoreflect.MessageDescriptor) bool {
	return isTimestamp(desc) ||
		isDuration(desc) ||
		isWrapperType(desc) ||
		isBigInt(desc) ||
		isDecimal(desc) ||
		isBigFloat(desc)
}

// msgValueForms names the value forms a message-typed field admits, for
// the error raised when the token at the value position matched none of
// them. Every message field takes the block form, so "'{'" is the floor;
// the well-known types that also take a literal say so, because a reader
// who wrote `dur_field = 1.5x` meant a duration and is not helped by
// being told to open a brace. Mirrors the shorthand branches of
// decodeMsgValue / consumeListMsg / decodeMapInline — a shorthand added
// there needs a form named here.
//
// Wrapper types never reach this: they route any non-'{' token to their
// inner scalar field, which reports the inner type itself.
func msgValueForms(desc protoreflect.MessageDescriptor) string {
	switch {
	case isTimestamp(desc):
		return "a timestamp literal or '{'"
	case isDuration(desc):
		return "a duration literal or '{'"
	case isBigInt(desc):
		return "an integer literal or '{'"
	case isDecimal(desc), isBigFloat(desc):
		return "a numeric literal or '{'"
	case isSecret(desc):
		return "a string literal or '{'"
	}
	return "'{'"
}

// pxf.Secret well-known type.
//
// Secret carries a sensitive scalar plus optional hint/fingerprint
// metadata. PXF accepts two surface forms:
//
//	pw = "x"                              # scalar shorthand
//	pw { value = "x", hint = "h" }        # explicit block
//
// The codec treats the inner `value` field as a string for transport
// purposes. Memguard handoff happens in the consumer (chameleon
// resolver), not here — this codec only routes bytes between the wire
// and the proto message. Keeping that boundary clean lets protowire-go
// stay free of any memguard dependency.

func isSecret(desc protoreflect.MessageDescriptor) bool {
	return desc.FullName() == "pxf.Secret" && hasKnownShape(desc)
}

// secretHasMetadata reports whether the Secret message carries hint or
// fingerprint, in which case the encoder must emit the explicit block
// form to round-trip those fields. Empty metadata → scalar shorthand.
//
// Note: there is no setSecretValue / readSecretValue helper here.
// Secret.value is a plain string field, so the codec uses the existing
// scalar paths (consumeScalar / writeScalar) directly — adding helpers
// that wrap them would either bypass UTF-8 validation or duplicate
// logic. Compare with pxf.BigInt etc. which DO need helpers because
// their inner representation is multi-field (abs + negative bytes).
func secretHasMetadata(msg protoreflect.Message) bool {
	d := msg.Descriptor()
	if hf := d.Fields().ByName("hint"); hf != nil && msg.Get(hf).String() != "" {
		return true
	}
	if ff := d.Fields().ByName("fingerprint"); ff != nil && msg.Get(ff).String() != "" {
		return true
	}
	return false
}

// Big number field setters (decode direction).

func setBigIntFields(msg protoreflect.Message, v *big.Int) {
	d := msg.Descriptor()
	absBytes := v.Bytes() // unsigned big-endian
	if len(absBytes) > 0 {
		msg.Set(d.Fields().ByName("abs"), protoreflect.ValueOfBytes(absBytes))
	}
	if v.Sign() < 0 {
		msg.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(true))
	}
}

func setDecimalFields(msg protoreflect.Message, unscaled *big.Int, scale int32, negative bool) {
	d := msg.Descriptor()
	absBytes := unscaled.Bytes()
	if len(absBytes) > 0 {
		msg.Set(d.Fields().ByName("unscaled"), protoreflect.ValueOfBytes(absBytes))
	}
	if scale != 0 {
		msg.Set(d.Fields().ByName("scale"), protoreflect.ValueOfInt32(scale))
	}
	if negative {
		msg.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(true))
	}
}

func setBigFloatFields(msg protoreflect.Message, v *big.Float) {
	d := msg.Descriptor()
	prec := v.Prec()
	// A zero keeps the precision its literal stated and its sign, with an
	// empty mantissa and exponent 0 (draft -01 § PXF Annotations,
	// protowire#410): parseBigFloat hands back prec 0 for a zero written
	// without a suffix. It used to be stored at the literal's precision,
	// with exponent -prec.
	if prec == 0 || v.Sign() == 0 {
		if prec > 0 {
			msg.Set(d.Fields().ByName("prec"), protoreflect.ValueOfUint32(uint32(prec)))
		}
		if v.Signbit() {
			msg.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(true))
		}
		return
	}
	// Extract mantissa as integer: multiply by 2^prec and take Int part.
	// MantExp returns mant in [0.5, 1.0) and exp such that v = mant × 2^exp.
	mant := new(big.Float).SetPrec(prec)
	exp := v.MantExp(mant)
	mant.SetMantExp(mant, int(prec)) // mant × 2^prec → integer
	mantInt, _ := mant.Int(nil)
	if mantInt.Sign() < 0 {
		mantInt.Neg(mantInt)
	}
	mantBytes := mantInt.Bytes()
	if len(mantBytes) > 0 {
		msg.Set(d.Fields().ByName("mantissa"), protoreflect.ValueOfBytes(mantBytes))
	}
	// Adjusted exponent: v = mantInt × 2^(exp - prec)
	adjExp := int32(exp) - int32(prec)
	if adjExp != 0 {
		msg.Set(d.Fields().ByName("exponent"), protoreflect.ValueOfInt32(adjExp))
	}
	msg.Set(d.Fields().ByName("prec"), protoreflect.ValueOfUint32(uint32(prec)))
	if v.Signbit() {
		msg.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(true))
	}
}

// Big number field readers (encode direction).

func readBigInt(msg protoreflect.Message) *big.Int {
	d := msg.Descriptor()
	absBytes := msg.Get(d.Fields().ByName("abs")).Bytes()
	negative := msg.Get(d.Fields().ByName("negative")).Bool()
	v := new(big.Int).SetBytes(absBytes)
	if negative {
		v.Neg(v)
	}
	return v
}

// bigFloatValueExponent returns the binary exponent of the value a
// mantissa and wire exponent denote, the exponent plus the mantissa's bit
// length, and false for a zero mantissa, which has none. The bit length
// is read off the bytes, so no big.Int is built for it.
func bigFloatValueExponent(mantissa []byte, exponent int64) (int64, bool) {
	for len(mantissa) > 0 && mantissa[0] == 0 {
		mantissa = mantissa[1:]
	}
	if len(mantissa) == 0 {
		return 0, false
	}
	return exponent + int64(8*(len(mantissa)-1)+bits.Len8(mantissa[0])), true
}

// bigFloatExponentExceeds reports whether a value's binary exponent is
// past limit in magnitude (MaxBigFloatExponent, protowire#281). Every
// reader and writer of a pxf.BigFloat here asks it, each in its own
// words.
func bigFloatExponentExceeds(e int64, limit int) bool {
	return e > int64(limit) || e < -int64(limit)
}

func readBigFloat(msg protoreflect.Message) *big.Float {
	d := msg.Descriptor()
	mantBytes := msg.Get(d.Fields().ByName("mantissa")).Bytes()
	exp := int(msg.Get(d.Fields().ByName("exponent")).Int())
	prec := uint(msg.Get(d.Fields().ByName("prec")).Uint())
	negative := msg.Get(d.Fields().ByName("negative")).Bool()

	if prec == 0 {
		return new(big.Float)
	}
	mantInt := new(big.Int).SetBytes(mantBytes)
	bf := new(big.Float).SetPrec(prec).SetInt(mantInt)
	// v = mantInt × 2^exponent, but mantInt was stored as mant × 2^prec,
	// so we need SetMantExp with exp (which is already adjusted: original_exp - prec + prec from SetInt)
	// SetInt gives bf = mantInt = mant × 2^prec
	// We need bf = mant × 2^original_exp = mantInt × 2^(original_exp - prec) = mantInt × 2^adjExp
	bf.SetMantExp(bf, exp) // bf = mantInt × 2^adjExp
	if negative {
		bf.Neg(bf)
	}
	return bf
}

// formatBigInt renders a BigInt message as the integer literal parseBigInt
// takes. The work is proportional to abs's length, so the literal is
// bounded after rendering, not before.
func formatBigInt(msg protoreflect.Message) (string, error) {
	return boundLiteral("pxf.BigInt", readBigInt(msg).Text(10))
}

// boundLiteral is the marshaller's half of MaxNumericLiteralDigits: a
// literal this package's own decoder would refuse is an error here
// rather than a document nobody can read (#119). HARDENING.md
// § Arbitrary-precision magnitudes bounds a marshaller's literal "like
// any other", and the pb marshaller refuses a Decimal.scale past the
// same constant for the same reason (#95). It counts what
// checkLiteralDigits counts (countDigits); a literal short enough to be
// within the limit is not counted at all.
func boundLiteral(kind, s string) (string, error) {
	if len(s) <= MaxNumericLiteralDigits {
		return s, nil
	}
	if n := countDigits(s); n > MaxNumericLiteralDigits {
		return "", fmt.Errorf("%s renders to %d digits; MaxNumericLiteralDigits=%d", kind, n, MaxNumericLiteralDigits)
	}
	return s, nil
}

// readDecimalStr renders a Decimal message as the positional literal
// parseDecimal takes. value = unscaled × 10^(-scale) (pxf/bignum.proto):
// a positive scale places the point, and a NEGATIVE scale means
// trailing zeros — unscaled 5, scale -2 is 500 — which is how the pb
// decoder and the @default carrier reader have read it since #92; the
// encoder wrote the digits alone and dropped the zeros (#118).
//
// The bound comes before the value: the negative arm materialises
// 10^|scale| and the positive arm pads to |scale| digits, and the scale
// is four bytes whoever wrote the message chose, so the work would be
// proportional to a number rather than to the input's length (#95).
// HARDENING.md bounds the magnitude of Decimal.scale by
// MaxNumericLiteralDigits, on both signs, before anything is
// materialised; this is the same bound as the two readers'. encoding/pxf
// never writes a scale outside it — parseDecimal yields a fraction's
// digit count, already under the cap and never negative — so both arms
// exist for bytes from another producer.
func readDecimalStr(msg protoreflect.Message) (string, error) {
	d := msg.Descriptor()
	unscaledBytes := msg.Get(d.Fields().ByName("unscaled")).Bytes()
	scale := msg.Get(d.Fields().ByName("scale")).Int()
	negative := msg.Get(d.Fields().ByName("negative")).Bool()

	if scale > MaxNumericLiteralDigits || scale < -MaxNumericLiteralDigits {
		return "", fmt.Errorf("pxf.Decimal scale %d exceeds MaxNumericLiteralDigits=%d", scale, MaxNumericLiteralDigits)
	}
	unscaled := new(big.Int).SetBytes(unscaledBytes)
	if scale < 0 {
		// -scale on the int64 the reflection API hands back: negating
		// MinInt32 as an int32 would be MinInt32 again. Unreachable past
		// the bound above; kept correct anyway.
		unscaled.Mul(unscaled, new(big.Int).Exp(big.NewInt(10), big.NewInt(-scale), nil))
		scale = 0
	}
	digits := unscaled.Text(10)

	var buf strings.Builder
	if negative {
		buf.WriteByte('-')
	}
	if scale == 0 {
		buf.WriteString(digits)
		return boundLiteral("pxf.Decimal", buf.String())
	}
	// Pad with leading zeros if needed (e.g. unscaled=5, scale=2 → "0.05")
	if pad := int(scale) - len(digits) + 1; pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	intPart := digits[:len(digits)-int(scale)]
	fracPart := digits[len(digits)-int(scale):]
	buf.WriteString(intPart)
	buf.WriteByte('.')
	buf.WriteString(fracPart)
	// The scale bound above keeps the work proportional to the input;
	// this keeps the literal readable. Leading zeros are not significant
	// (protowire#410, reversing in part #310), so unscaled 5 at scale
	// 4096, 0.000…5, counts one digit and has a literal; only the
	// negative arm's trailing zeros can carry a literal past the bound.
	return boundLiteral("pxf.Decimal", buf.String())
}

// formatBigFloat renders a BigFloat message as the literal parseBigFloat
// takes, in exponent form at the precision the message declares.
func formatBigFloat(msg protoreflect.Message) (string, error) {
	// A precision of zero is zero whatever the other fields say, as it
	// is to readBigFloat and to encoding/pb's Unmarshal, so it has no
	// exponent to bound.
	d := msg.Descriptor()
	if msg.Get(d.Fields().ByName("prec")).Uint() == 0 {
		// A prec-0 zero is the one BigFloat written without a suffix
		// (protowire#410), its sign kept.
		if msg.Get(d.Fields().ByName("negative")).Bool() {
			return "-0", nil
		}
		return "0", nil
	}
	// Rendering costs time that grows with the square of |exponent|, so
	// the bound comes before anything is built (MaxBigFloatExponent,
	// protowire#281). A message past it has no PXF literal: an error,
	// not a substituted value.
	mantissa := msg.Get(d.Fields().ByName("mantissa")).Bytes()
	if e, ok := bigFloatValueExponent(mantissa, msg.Get(d.Fields().ByName("exponent")).Int()); ok && bigFloatExponentExceeds(e, MaxBigFloatExponent) {
		return "", fmt.Errorf("pxf.BigFloat binary exponent %d exceeds MaxBigFloatExponent=%d", e, MaxBigFloatExponent)
	}
	bf := readBigFloat(msg)
	prec := bf.Prec()
	// The bound applies to the value after rounding to prec (protowire#410),
	// and rounding can carry a mantissa to the next power of two: 2^54-1
	// at prec 3 is 2^54. Such a value reads off the wire at the bound, and
	// its literal is past it, which every reader refuses, so the producer
	// fails (draft -01 § Mandatory Limits, #261).
	if e := int64(bf.MantExp(nil)); bf.Sign() != 0 && bigFloatExponentExceeds(e, MaxBigFloatExponent) {
		return "", fmt.Errorf("pxf.BigFloat binary exponent %d after rounding to prec %d exceeds MaxBigFloatExponent=%d", e, prec, MaxBigFloatExponent)
	}
	// prec is four bytes the message's producer chose, and Text works
	// for every digit it is asked for, so the digit count is bounded
	// before rendering: past MaxNumericLiteralDigits the literal could
	// not be read anyway. This bounds the mantissa's cost; the check
	// above bounds the exponent's.
	decDigits, ok := bigFloatDigits(uint32(prec), MaxNumericLiteralDigits)
	if !ok {
		return "", fmt.Errorf("pxf.BigFloat prec %d bits renders to %d digits; MaxNumericLiteralDigits=%d", prec, decDigits, MaxNumericLiteralDigits)
	}
	// bigFloatText writes at most decDigits significant digits, and the
	// exponent and suffix are not counted (protowire#410), so the literal
	// is within MaxNumericLiteralDigits without boundLiteral.
	return bigFloatText(bf, decDigits), nil
}

// bigFloatText writes bf, whose precision is not 0, as a PXF BigFloat
// literal with digits significant digits and its precision suffix (draft
// -01 § PXF Annotations, protowire#406): C's %g form, then "p" and bf's
// precision, so a decoder reads it back at that precision and gets bf
// exactly. A zero keeps its precision and sign the same way, "0p64" or
// "-0p64" (protowire#410). The one literal without a suffix, the prec-0
// zero, is its callers' to write: formatBigFloat writes "0" or "-0", and
// a carrier has no prec-0 value.
func bigFloatText(bf *big.Float, digits int) string {
	suffix := "p" + strconv.FormatUint(uint64(bf.Prec()), 10)
	if bf.Sign() == 0 {
		if bf.Signbit() {
			return "-0" + suffix
		}
		return "0" + suffix
	}
	return bf.Text('g', digits) + suffix
}

// bigFloatDigits is the number of significant digits a BigFloat of prec
// bits is written with, and whether that is within limit (draft -01 § PXF
// Annotations, protowire#406). encoding/pb bounds a wire prec with the same
// count (#499), so it lives in internal/bigprec.
func bigFloatDigits(prec uint32, limit int) (int, bool) {
	return bigprec.Digits(prec, limit)
}

// Big number string parsers (decode direction).

// checkLiteralDigits enforces MaxNumericLiteralDigits on a literal
// about to be handed to a big-number parser. Digits are what the limit
// counts, so a sign, a point or an exponent marker does not — but a
// literal short enough to be within the limit needs no counting at all,
// and that is every literal a real document carries.
func checkLiteralDigits(s string, maxDigits int) error {
	if len(s) <= maxDigits {
		return nil
	}
	if n := countDigits(s); n > maxDigits {
		return fmt.Errorf("numeric literal has %d digits; MaxNumericLiteralDigits=%d", n, maxDigits)
	}
	return nil
}

// countDigits is what MaxNumericLiteralDigits counts: the significant
// digits of a literal's mantissa, either side of the point, leading zeros
// excluded (draft -01 § Mandatory Limits, protowire#410). An exponent and
// a precision suffix are bounded on their own, by MaxBigFloatExponent and
// by the N a suffix implies, so the count stops at either marker.
func countDigits(s string) int {
	n := 0
	leading := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 'e' || c == 'E' || c == 'p' {
			break
		}
		if c < '0' || c > '9' || (leading && c == '0') {
			continue
		}
		leading = false
		n++
	}
	return n
}

// clipLiteral shortens a literal for an error message. The literals that
// reach the errors below can be thousands of digits long, and an error
// is read, not parsed.
func clipLiteral(s string) string {
	if len(s) <= 24 {
		return s
	}
	return s[:24] + "…"
}

func parseBigInt(s string, maxDigits int) (*big.Int, error) {
	if err := checkLiteralDigits(s, maxDigits); err != nil {
		return nil, err
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid big integer: %s", s)
	}
	return v, nil
}

func parseDecimal(s string, maxDigits int) (unscaled *big.Int, scale int32, negative bool, err error) {
	if err := checkLiteralDigits(s, maxDigits); err != nil {
		return nil, 0, false, err
	}
	raw := s
	if len(s) > 0 && s[0] == '-' {
		negative = true
		s = s[1:]
	}
	dotIdx := strings.IndexByte(s, '.')
	if dotIdx >= 0 {
		scale = int32(len(s) - dotIdx - 1)
		s = s[:dotIdx] + s[dotIdx+1:]
	}
	// The digit cap no longer bounds the scale: leading zeros are not
	// significant (protowire#410), so "0.000…1" counts one digit however
	// many places it has. The scale is bounded as it is on the PB wire
	// (encoding/pb): MaxNumericLiteralDigits, inclusive.
	if int(scale) > maxDigits {
		return nil, 0, false, fmt.Errorf("pxf.Decimal scale %d exceeds MaxNumericLiteralDigits=%d", scale, maxDigits)
	}
	unscaled, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, 0, false, fmt.Errorf("invalid decimal: %s", raw)
	}
	return unscaled, scale, negative, nil
}

// bigFloatDefaultPrec is the precision a PXF BigFloat literal without a
// precision suffix is read at (draft -01 § PXF Annotations,
// protowire#406): what a person writes by hand, read as it always was.
const bigFloatDefaultPrec = 256

// parseBigFloat reads a BigFloat literal, s, with or without its
// precision suffix "p<bits>" (protowire#406), for a literal that arrives
// as one string, a carrier's. A document's literal arrives lexed, number
// and suffix apart, and goes to parseBigFloatPrec.
func parseBigFloat(s string, maxDigits, maxExp int) (*big.Float, error) {
	num, prec, suffixed := strings.Cut(s, "p")
	if suffixed && prec == "" {
		return nil, fmt.Errorf("big float literal %s: precision suffix p is not a positive bit count", clipLiteral(s))
	}
	return parseBigFloatPrec(num, prec, maxDigits, maxExp)
}

// parseBigFloatPrec reads a BigFloat literal, s, at the precision its
// suffix's digits, prec, state, or at bigFloatDefaultPrec when prec is ""
// (protowire#406), rounding to nearest with ties to even. The suffix is
// checked before anything is allocated for it: a zero precision, or one
// whose literal would need more than maxDigits digits, is an error. A
// suffix of more digits than a uint32 has is rejected without being
// converted.
func parseBigFloatPrec(s, prec string, maxDigits, maxExp int) (*big.Float, error) {
	readPrec := uint(bigFloatDefaultPrec)
	suffixed := prec != ""
	if suffixed {
		p, err := uint64(0), error(nil)
		if len(prec) <= len("4294967295") {
			p, err = strconv.ParseUint(prec, 10, 32)
		}
		if p == 0 || err != nil {
			return nil, fmt.Errorf("big float literal %sp%s: precision suffix p%s is not a positive bit count", clipLiteral(s), clipLiteral(prec), clipLiteral(prec))
		}
		if n, ok := bigFloatDigits(uint32(p), maxDigits); !ok {
			return nil, fmt.Errorf("big float literal %sp%d: precision p%d renders to %d digits; MaxNumericLiteralDigits=%d", clipLiteral(s), p, p, n, maxDigits)
		}
		readPrec = uint(p)
	}
	if err := checkLiteralDigits(s, maxDigits); err != nil {
		return nil, err
	}
	bf, _, err := new(big.Float).SetPrec(readPrec).SetMode(big.ToNearestEven).Parse(s, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid big float: %s", s)
	}
	// big.Float.Parse substitutes ±Inf for a value above its range and
	// ±0 for one below it, and reports neither. A finite, non-zero
	// literal that came back as either is a value pxf.BigFloat cannot
	// carry, which draft -01 §mandatory-limits makes an error rather
	// than a substitution (protowire#278). The overflow used to reach
	// setBigFloatFields, whose mant.Int returned nil for +Inf — a panic
	// on a sixteen-byte document.
	if bf.IsInf() {
		return nil, fmt.Errorf("big float literal %s is above big.Float's range", clipLiteral(s))
	}
	if bf.Sign() == 0 && hasNonZeroMantissaDigit(s) {
		return nil, fmt.Errorf("big float literal %s is below big.Float's range", clipLiteral(s))
	}
	// setBigFloatFields scales a value up to prec bits, which leaves
	// big.Float's range at 2^31 bits or more: its mant.Int then returns nil
	// (#229). Only a MaxNumericLiteralDigits raised past 646 million admits
	// a suffix that wide. A zero has no mantissa to scale.
	if bf.Sign() != 0 && readPrec > math.MaxInt32 {
		return nil, fmt.Errorf("big float literal %s: precision p%d exceeds the widest mantissa big.Float can hold, %d bits", clipLiteral(s), readPrec, math.MaxInt32)
	}
	// The wire carries exp - prec as an int32 (pxf/bignum.proto). A value
	// near big.Float's floor parses but cannot be encoded, and the int32
	// subtraction in setBigFloatFields wrapped silently. A zero is stored
	// with exponent 0 at any precision.
	if bf.Sign() != 0 && int64(bf.MantExp(nil))-int64(readPrec) < math.MinInt32 {
		return nil, fmt.Errorf("big float literal %s is below the wire's exponent range", clipLiteral(s))
	}
	// The value's binary exponent is bounded as one read off the wire is
	// (MaxBigFloatExponent, protowire#281). Zero has none.
	if e := bf.MantExp(nil); bf.Sign() != 0 && bigFloatExponentExceeds(int64(e), maxExp) {
		return nil, fmt.Errorf("big float literal %s has binary exponent %d, and MaxBigFloatExponent=%d bounds its magnitude", clipLiteral(s), e, maxExp)
	}
	// big.Float.Parse scales a decimal exponent by powers of ten at a
	// working precision and then rounds again, so a value within a hair of
	// a tie can round the wrong way (#260). The draft rounds the literal's
	// exact value once, ties to even. The checks above bound the decimal
	// exponent and the digits, so the exact rational is of bounded size.
	// Rounding it can carry the exponent past the bound, so it is asked
	// again.
	if bf.Sign() != 0 {
		exact, ok := roundDecimalLiteral(s, readPrec)
		if !ok {
			return nil, fmt.Errorf("invalid big float: %s", s)
		}
		bf = exact
		e := bf.MantExp(nil)
		if bigFloatExponentExceeds(int64(e), maxExp) {
			return nil, fmt.Errorf("big float literal %s has binary exponent %d, and MaxBigFloatExponent=%d bounds its magnitude", clipLiteral(s), e, maxExp)
		}
		if int64(e)-int64(readPrec) < math.MinInt32 {
			return nil, fmt.Errorf("big float literal %s is below the wire's exponent range", clipLiteral(s))
		}
	}
	// A zero written without a suffix has no precision to keep: prec 0,
	// its sign kept (protowire#410).
	if bf.Sign() == 0 && !suffixed {
		z := new(big.Float)
		if bf.Signbit() {
			z.Neg(z)
		}
		bf = z
	}
	return bf, nil
}

// roundDecimalLiteral rounds s, a decimal literal big.Float.Parse has
// accepted, to prec bits from its exact value, ties to even (#260). The
// literal is an integer of its digits, d, scaled by 10^k. For k >= 0 the
// value d * 10^k is an integer and SetInt rounds it once. For k < 0 it is
// the quotient of two integers, each held exactly, and Quo rounds that
// quotient once: big.Float arithmetic is correctly rounded. ok is false
// for a form this does not read, which Parse would not have accepted.
func roundDecimalLiteral(s string, prec uint) (*big.Float, bool) {
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	k := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		n, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return nil, false
		}
		k, s = n, s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		k -= len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	d, ok := new(big.Int).SetString(s, 10)
	if !ok || d.Sign() < 0 {
		return nil, false
	}
	z := new(big.Float).SetPrec(prec).SetMode(big.ToNearestEven)
	if k >= 0 {
		z.SetInt(d.Mul(d, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil)))
	} else {
		num := new(big.Float).SetInt(d) // exact: SetInt takes the bits it needs
		den := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-k)), nil))
		z.Quo(num, den)
	}
	if neg {
		z.Neg(z)
	}
	return z, true
}

// hasNonZeroMantissaDigit reports whether the literal's mantissa — the
// part before any exponent marker — has a digit other than 0, so that a
// parse result of zero can be told apart from an underflow.
func hasNonZeroMantissaDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == 'e' || c == 'E':
			return false
		case c >= '1' && c <= '9':
			return true
		}
	}
	return false
}
