// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package pb provides generic protobuf binary marshaling for native Go structs.
//
// Field numbers are specified via struct tags:
//
//	type Message struct {
//	    Name  string `protowire:"1"`
//	    Count int    `protowire:"2"`
//	    Delta int64  `protowire:"3,zigzag"`  // proto3 sint64 (zigzag varint)
//	}
//
// The encoding follows proto3 semantics: zero-value fields are omitted.
// Signed-integer fields default to proto3 int32 / int64 wire format
// (plain varint, with negative values sign-extended to a 10-byte uint64).
// The `zigzag` tag option selects proto3 sint32 / sint64 instead, which
// is more compact for negative values.
//
// The wire format is standard protobuf binary, compatible with any .proto
// definition using the same field numbers and matching types.
//
// # Concurrency
//
// The package-level [Marshal] and [Unmarshal] functions are safe for
// concurrent use — they do not share mutable state. The struct-tag
// reflection cache is internally synchronized.
//
// [Encoder] and [Decoder] (the length-prefixed stream wrappers) hold a
// reusable scratch buffer and are not safe for concurrent use; create
// one per goroutine, or guard with a mutex.
package pb

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

var (
	bigIntType   = reflect.TypeOf(big.Int{})
	bigRatType   = reflect.TypeOf(big.Rat{})
	bigFloatType = reflect.TypeOf(big.Float{})
)

// MaxNestingDepth caps PB submessage / map-entry recursion per
// protowire/docs/HARDENING.md § Recursion. The counter is threaded
// through unmarshalStruct so it survives nested-stream construction
// (each embedded message decode opens a fresh sub-buffer but inherits
// the parent's depth).
const MaxNestingDepth = 100

// Marshal encodes a struct into protobuf binary format.
// v must be a pointer to a struct with protowire:"N" tags.
func Marshal(v any) ([]byte, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("pb.Marshal: expected struct, got %s", rv.Kind())
	}
	return marshalStruct(rv)
}

// Unmarshal decodes protobuf binary into a struct.
// v must be a pointer to a struct with protowire:"N" tags.
func Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("pb.Unmarshal: expected pointer to struct, got %s", rv.Type())
	}
	return unmarshalStruct(data, rv.Elem(), 1)
}

// fieldInfo caches parsed struct tag info for a field.
type fieldInfo struct {
	index    int              // struct field index
	fieldNum protowire.Number // protobuf field number
	zigzag   bool             // signed ints use zigzag (proto3 sint32/sint64)
}

// structInfo caches all tagged fields for a struct type.
type structInfo struct {
	fields   []fieldInfo                    // all tagged fields
	byNumber map[protowire.Number]fieldInfo // lookup by field number
}

var structCache sync.Map // map[reflect.Type]*structInfo

func getStructInfo(t reflect.Type) *structInfo {
	if v, ok := structCache.Load(t); ok {
		return v.(*structInfo)
	}

	info := &structInfo{
		byNumber: make(map[protowire.Number]fieldInfo),
	}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("protowire")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		num, err := strconv.Atoi(parts[0])
		if err != nil || num < 1 {
			continue
		}
		fi := fieldInfo{index: i, fieldNum: protowire.Number(num)}
		for _, opt := range parts[1:] {
			if opt == "zigzag" {
				fi.zigzag = true
			}
		}
		info.fields = append(info.fields, fi)
		info.byNumber[fi.fieldNum] = fi
	}

	v, _ := structCache.LoadOrStore(t, info)
	return v.(*structInfo)
}

func marshalStruct(rv reflect.Value) ([]byte, error) {
	info := getStructInfo(rv.Type())
	var b []byte

	for _, fi := range info.fields {
		fv := rv.Field(fi.index)
		var err error
		b, err = marshalField(b, fi.fieldNum, fv, fi.zigzag)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", rv.Type().Field(fi.index).Name, err)
		}
	}
	return b, nil
}

func marshalField(b []byte, num protowire.Number, fv reflect.Value, zigzag bool) ([]byte, error) {
	// Handle pointer: dereference, skip if nil
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return b, nil
		}
		fv = fv.Elem()
	}

	// Handle slices (repeated fields)
	if fv.Kind() == reflect.Slice {
		// []byte is a scalar (bytes field), not repeated
		if fv.Type().Elem().Kind() == reflect.Uint8 && fv.Type() == reflect.TypeOf([]byte(nil)) {
			if fv.Len() == 0 {
				return b, nil
			}
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, fv.Bytes())
			return b, nil
		}
		// Repeated: one tag+value per element
		for i := 0; i < fv.Len(); i++ {
			var err error
			b, err = marshalField(b, num, fv.Index(i), zigzag)
			if err != nil {
				return nil, err
			}
		}
		return b, nil
	}

	// Scalar and message fields — skip zero values (proto3)
	switch fv.Kind() {
	case reflect.Bool:
		if !fv.Bool() {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)

	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		v := fv.Int()
		if v == 0 {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.VarintType)
		if zigzag {
			b = protowire.AppendVarint(b, protowire.EncodeZigZag(v))
		} else {
			// proto3 int32/int64: plain varint with sign-extension to uint64.
			b = protowire.AppendVarint(b, uint64(v))
		}

	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		v := fv.Uint()
		if v == 0 {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.VarintType)
		b = protowire.AppendVarint(b, v)

	case reflect.Float64:
		v := fv.Float()
		if v == 0 {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, math.Float64bits(v))

	case reflect.Float32:
		v := float32(fv.Float())
		if v == 0 {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.Fixed32Type)
		b = protowire.AppendFixed32(b, math.Float32bits(v))

	case reflect.String:
		v := fv.String()
		if v == "" {
			return b, nil
		}
		b = protowire.AppendTag(b, num, protowire.BytesType)
		b = protowire.AppendString(b, v)

	case reflect.Struct:
		switch fv.Type() {
		case bigIntType:
			bi := fv.Addr().Interface().(*big.Int)
			if bi.Sign() == 0 {
				return b, nil
			}
			msg := marshalBigInt(bi)
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, msg)
		case bigRatType:
			rat := fv.Addr().Interface().(*big.Rat)
			if rat.Sign() == 0 {
				return b, nil
			}
			msg := marshalBigRat(rat)
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, msg)
		case bigFloatType:
			bf := fv.Addr().Interface().(*big.Float)
			if bf.Sign() == 0 && bf.Prec() == 0 {
				return b, nil
			}
			msg := marshalBigFloat(bf)
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, msg)
		default:
			msg, err := marshalStruct(fv)
			if err != nil {
				return nil, err
			}
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, msg)
		}

	case reflect.Map:
		// proto3 maps: each entry is a length-prefixed MapEntry message
		// with key at field 1 and value at field 2. Map keys/values inherit
		// the parent field's zigzag flag (uncommon — map keys are usually
		// strings or unsigned ints).
		if fv.Len() == 0 {
			return b, nil
		}
		iter := fv.MapRange()
		for iter.Next() {
			var entry []byte
			var err error
			entry, err = marshalField(entry, 1, iter.Key(), zigzag)
			if err != nil {
				return nil, fmt.Errorf("map key: %w", err)
			}
			entry, err = marshalField(entry, 2, iter.Value(), zigzag)
			if err != nil {
				return nil, fmt.Errorf("map value: %w", err)
			}
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendBytes(b, entry)
		}

	default:
		return nil, fmt.Errorf("unsupported kind %s", fv.Kind())
	}

	return b, nil
}

// unmarshalStruct decodes data into rv. depth is the current submessage
// depth (top-level call = 1); it is threaded through nested stream
// construction so a fresh inner buffer cannot reset the recursion counter.
func unmarshalStruct(data []byte, rv reflect.Value, depth int) error {
	if depth > MaxNestingDepth {
		return fmt.Errorf("nesting depth exceeds MaxNestingDepth=%d", MaxNestingDepth)
	}
	info := getStructInfo(rv.Type())

	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("corrupt tag")
		}
		data = data[n:]

		fi, ok := info.byNumber[num]
		if !ok {
			// Skip unknown field
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return fmt.Errorf("corrupt field %d", num)
			}
			data = data[n:]
			continue
		}

		fv := rv.Field(fi.index)
		consumed, err := unmarshalField(data, num, typ, fv, fi.zigzag, depth)
		if err != nil {
			return fmt.Errorf("field %s: %w", rv.Type().Field(fi.index).Name, err)
		}
		data = data[consumed:]
	}
	return nil
}

func unmarshalField(data []byte, num protowire.Number, typ protowire.Type, fv reflect.Value, zigzag bool, depth int) (int, error) {
	// Handle pointer: allocate if nil
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		fv = fv.Elem()
	}

	// Handle slices (repeated fields) — except []byte
	if fv.Kind() == reflect.Slice && !(fv.Type().Elem().Kind() == reflect.Uint8 && fv.Type() == reflect.TypeOf([]byte(nil))) {
		elem := reflect.New(fv.Type().Elem()).Elem()
		// If elem is a pointer, allocate it
		if elem.Kind() == reflect.Ptr {
			elem.Set(reflect.New(elem.Type().Elem()))
		}
		consumed, err := unmarshalField(data, num, typ, elem, zigzag, depth)
		if err != nil {
			return 0, err
		}
		fv.Set(reflect.Append(fv, elem))
		return consumed, nil
	}

	switch fv.Kind() {
	case reflect.Bool:
		v, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt bool")
		}
		fv.SetBool(v != 0)
		return n, nil

	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8:
		v, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt int")
		}
		if zigzag {
			fv.SetInt(protowire.DecodeZigZag(v))
		} else {
			// proto3 int32/int64: low N bits as signed.
			fv.SetInt(int64(v))
		}
		return n, nil

	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		v, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt uint")
		}
		fv.SetUint(v)
		return n, nil

	case reflect.Float64:
		v, n := protowire.ConsumeFixed64(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt float64")
		}
		fv.SetFloat(math.Float64frombits(v))
		return n, nil

	case reflect.Float32:
		v, n := protowire.ConsumeFixed32(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt float32")
		}
		fv.SetFloat(float64(math.Float32frombits(v)))
		return n, nil

	case reflect.String:
		v, n := protowire.ConsumeString(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt string")
		}
		fv.SetString(v)
		return n, nil

	case reflect.Slice:
		// Must be []byte
		v, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt bytes")
		}
		fv.SetBytes(append([]byte(nil), v...))
		return n, nil

	case reflect.Struct:
		v, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt embedded message")
		}
		switch fv.Type() {
		case bigIntType:
			bi := fv.Addr().Interface().(*big.Int)
			if err := unmarshalBigIntMsg(v, bi); err != nil {
				return 0, err
			}
		case bigRatType:
			rat := fv.Addr().Interface().(*big.Rat)
			if err := unmarshalBigRatMsg(v, rat); err != nil {
				return 0, err
			}
		case bigFloatType:
			bf := fv.Addr().Interface().(*big.Float)
			if err := unmarshalBigFloatMsg(v, bf); err != nil {
				return 0, err
			}
		default:
			if err := unmarshalStruct(v, fv, depth+1); err != nil {
				return 0, err
			}
		}
		return n, nil

	case reflect.Map:
		v, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return 0, fmt.Errorf("corrupt map entry")
		}
		if fv.IsNil() {
			fv.Set(reflect.MakeMap(fv.Type()))
		}
		key := reflect.New(fv.Type().Key()).Elem()
		val := reflect.New(fv.Type().Elem()).Elem()
		entry := v
		for len(entry) > 0 {
			enum, etyp, en := protowire.ConsumeTag(entry)
			if en < 0 {
				return 0, fmt.Errorf("corrupt tag in map entry")
			}
			entry = entry[en:]
			switch enum {
			case 1:
				consumed, err := unmarshalField(entry, enum, etyp, key, zigzag, depth+1)
				if err != nil {
					return 0, fmt.Errorf("map key: %w", err)
				}
				entry = entry[consumed:]
			case 2:
				consumed, err := unmarshalField(entry, enum, etyp, val, zigzag, depth+1)
				if err != nil {
					return 0, fmt.Errorf("map value: %w", err)
				}
				entry = entry[consumed:]
			default:
				cn := protowire.ConsumeFieldValue(enum, etyp, entry)
				if cn < 0 {
					return 0, fmt.Errorf("corrupt unknown field %d in map entry", enum)
				}
				entry = entry[cn:]
			}
		}
		fv.SetMapIndex(key, val)
		return n, nil

	default:
		return 0, fmt.Errorf("unsupported kind %s", fv.Kind())
	}
}

// Big number marshal helpers.
// Each encodes a big number as a nested protobuf message matching the pxf.BigInt/Decimal/BigFloat schemas.

func marshalBigInt(bi *big.Int) []byte {
	var msg []byte
	abs := new(big.Int).Abs(bi)
	if absBytes := abs.Bytes(); len(absBytes) > 0 {
		msg = protowire.AppendTag(msg, 1, protowire.BytesType)
		msg = protowire.AppendBytes(msg, absBytes)
	}
	if bi.Sign() < 0 {
		msg = protowire.AppendTag(msg, 2, protowire.VarintType)
		msg = protowire.AppendVarint(msg, 1)
	}
	return msg
}

func marshalBigRat(rat *big.Rat) []byte {
	unscaled, scale := ratToDecimal(rat)
	negative := rat.Sign() < 0
	if negative {
		unscaled.Neg(unscaled)
	}
	var msg []byte
	if absBytes := unscaled.Bytes(); len(absBytes) > 0 {
		msg = protowire.AppendTag(msg, 1, protowire.BytesType)
		msg = protowire.AppendBytes(msg, absBytes)
	}
	if scale != 0 {
		msg = protowire.AppendTag(msg, 2, protowire.VarintType)
		msg = protowire.AppendVarint(msg, protowire.EncodeZigZag(int64(scale)))
	}
	if negative {
		msg = protowire.AppendTag(msg, 3, protowire.VarintType)
		msg = protowire.AppendVarint(msg, 1)
	}
	return msg
}

func marshalBigFloat(bf *big.Float) []byte {
	prec := bf.Prec()
	if prec == 0 {
		return nil
	}
	mant := new(big.Float).SetPrec(prec)
	exp := bf.MantExp(mant)
	mant.SetMantExp(mant, int(prec))
	mantInt, _ := mant.Int(nil)
	if mantInt.Sign() < 0 {
		mantInt.Neg(mantInt)
	}

	var msg []byte
	if mantBytes := mantInt.Bytes(); len(mantBytes) > 0 {
		msg = protowire.AppendTag(msg, 1, protowire.BytesType)
		msg = protowire.AppendBytes(msg, mantBytes)
	}
	adjExp := int32(exp) - int32(prec)
	if adjExp != 0 {
		msg = protowire.AppendTag(msg, 2, protowire.VarintType)
		msg = protowire.AppendVarint(msg, protowire.EncodeZigZag(int64(adjExp)))
	}
	msg = protowire.AppendTag(msg, 3, protowire.VarintType)
	msg = protowire.AppendVarint(msg, uint64(prec))
	if bf.Signbit() {
		msg = protowire.AppendTag(msg, 4, protowire.VarintType)
		msg = protowire.AppendVarint(msg, 1)
	}
	return msg
}

// ratToDecimal converts a big.Rat to (unscaled, scale) where value = unscaled × 10^(-scale).
// If the rational is not a terminating decimal, scale is capped at 50.
func ratToDecimal(rat *big.Rat) (unscaled *big.Int, scale int32) {
	// Factor out 2s and 5s from denominator to determine scale.
	denom := new(big.Int).Set(rat.Denom())
	two := big.NewInt(2)
	five := big.NewInt(5)
	var twos, fives int32
	for new(big.Int).Rem(denom, two).Sign() == 0 {
		denom.Div(denom, two)
		twos++
	}
	for new(big.Int).Rem(denom, five).Sign() == 0 {
		denom.Div(denom, five)
		fives++
	}
	if denom.Cmp(big.NewInt(1)) == 0 {
		// Terminating decimal.
		if twos > fives {
			scale = twos
		} else {
			scale = fives
		}
	} else {
		// Non-terminating: use string representation capped at 50 digits.
		s := rat.FloatString(50)
		// Parse back to get unscaled + scale.
		neg := s[0] == '-'
		if neg {
			s = s[1:]
		}
		dotIdx := len(s)
		for i, c := range s {
			if c == '.' {
				dotIdx = i
				break
			}
		}
		if dotIdx < len(s) {
			scale = int32(len(s) - dotIdx - 1)
			s = s[:dotIdx] + s[dotIdx+1:]
		}
		unscaled, _ = new(big.Int).SetString(s, 10)
		if neg {
			unscaled.Neg(unscaled)
		}
		return unscaled, scale
	}
	// unscaled = num × 10^scale / denom = num × 2^(scale-twos) × 5^(scale-fives)
	unscaled = new(big.Int).Set(rat.Num())
	if d := scale - fives; d > 0 {
		unscaled.Mul(unscaled, new(big.Int).Exp(five, big.NewInt(int64(d)), nil))
	}
	if d := scale - twos; d > 0 {
		unscaled.Mul(unscaled, new(big.Int).Exp(two, big.NewInt(int64(d)), nil))
	}
	return unscaled, scale
}

// Big number unmarshal helpers.

func unmarshalBigIntMsg(data []byte, bi *big.Int) error {
	var absBytes []byte
	var negative bool
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("corrupt tag in BigInt")
		}
		data = data[n:]
		switch num {
		case 1:
			v, vn := protowire.ConsumeBytes(data)
			if vn < 0 {
				return fmt.Errorf("corrupt abs in BigInt")
			}
			absBytes = v
			data = data[vn:]
		case 2:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt negative in BigInt")
			}
			negative = v != 0
			data = data[vn:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return fmt.Errorf("corrupt field %d in BigInt", num)
			}
			data = data[n:]
		}
	}
	bi.SetBytes(absBytes)
	if negative {
		bi.Neg(bi)
	}
	return nil
}

func unmarshalBigRatMsg(data []byte, rat *big.Rat) error {
	var unscaledBytes []byte
	var scale int32
	var negative bool
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("corrupt tag in Decimal")
		}
		data = data[n:]
		switch num {
		case 1:
			v, vn := protowire.ConsumeBytes(data)
			if vn < 0 {
				return fmt.Errorf("corrupt unscaled in Decimal")
			}
			unscaledBytes = v
			data = data[vn:]
		case 2:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt scale in Decimal")
			}
			scale = int32(protowire.DecodeZigZag(v))
			data = data[vn:]
		case 3:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt negative in Decimal")
			}
			negative = v != 0
			data = data[vn:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return fmt.Errorf("corrupt field %d in Decimal", num)
			}
			data = data[n:]
		}
	}
	unscaled := new(big.Int).SetBytes(unscaledBytes)
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	rat.SetFrac(unscaled, denom)
	if negative {
		rat.Neg(rat)
	}
	return nil
}

func unmarshalBigFloatMsg(data []byte, bf *big.Float) error {
	var mantBytes []byte
	var exp int32
	var prec uint
	var negative bool
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("corrupt tag in BigFloat")
		}
		data = data[n:]
		switch num {
		case 1:
			v, vn := protowire.ConsumeBytes(data)
			if vn < 0 {
				return fmt.Errorf("corrupt mantissa in BigFloat")
			}
			mantBytes = v
			data = data[vn:]
		case 2:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt exponent in BigFloat")
			}
			exp = int32(protowire.DecodeZigZag(v))
			data = data[vn:]
		case 3:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt prec in BigFloat")
			}
			prec = uint(v)
			data = data[vn:]
		case 4:
			v, vn := protowire.ConsumeVarint(data)
			if vn < 0 {
				return fmt.Errorf("corrupt negative in BigFloat")
			}
			negative = v != 0
			data = data[vn:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return fmt.Errorf("corrupt field %d in BigFloat", num)
			}
			data = data[n:]
		}
	}
	if prec == 0 {
		bf.SetFloat64(0)
		return nil
	}
	mantInt := new(big.Int).SetBytes(mantBytes)
	bf.SetPrec(prec).SetInt(mantInt)
	bf.SetMantExp(bf, int(exp))
	if negative {
		bf.Neg(bf)
	}
	return nil
}
