// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"math/big"
	"reflect"
	"testing"
)

type Inner struct {
	Name  string `protowire:"1"`
	Value int    `protowire:"2"`
}

type Outer struct {
	Title    string   `protowire:"1"`
	Count    uint32   `protowire:"2"`
	Score    float64  `protowire:"3"`
	Active   bool     `protowire:"4"`
	Data     []byte   `protowire:"5"`
	Items    []Inner  `protowire:"6"`
	Ptrs     []*Inner `protowire:"7"`
	Signed   int64    `protowire:"8"`
	SmallF   float32  `protowire:"9"`
	Untagged string   // no protowire tag — should be skipped
}

func TestRoundTrip(t *testing.T) {
	orig := &Outer{
		Title:  "hello",
		Count:  42,
		Score:  3.14,
		Active: true,
		Data:   []byte{0xDE, 0xAD},
		Items: []Inner{
			{Name: "a", Value: 1},
			{Name: "b", Value: -7},
		},
		Ptrs: []*Inner{
			{Name: "p", Value: 99},
		},
		Signed:   -12345,
		SmallF:   2.5,
		Untagged: "should be ignored",
	}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Outer{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Untagged field should not round-trip
	orig.Untagged = ""
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
}

func TestZeroValues(t *testing.T) {
	orig := &Outer{} // all zero

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Proto3: all-zero struct should produce empty bytes
	if len(data) != 0 {
		t.Errorf("expected empty bytes for zero struct, got %d bytes", len(data))
	}

	got := &Outer{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
}

type CustomByte byte

type WithCustomByte struct {
	Tag   CustomByte `protowire:"1"`
	Value int64      `protowire:"2"`
}

func TestCustomByteType(t *testing.T) {
	orig := &WithCustomByte{Tag: 3, Value: -42}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &WithCustomByte{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if *orig != *got {
		t.Errorf("mismatch: orig=%+v got=%+v", orig, got)
	}
}

func TestUnknownFieldsSkipped(t *testing.T) {
	// Encode a larger struct
	type Big struct {
		A string `protowire:"1"`
		B string `protowire:"2"`
		C string `protowire:"3"`
	}
	data, err := Marshal(&Big{A: "aa", B: "bb", C: "cc"})
	if err != nil {
		t.Fatal(err)
	}

	// Decode into a smaller struct — field 2 and 3 are unknown
	type Small struct {
		A string `protowire:"1"`
	}
	got := &Small{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal with unknown fields: %v", err)
	}
	if got.A != "aa" {
		t.Errorf("expected A=aa, got %s", got.A)
	}
}

type BigNumStruct struct {
	Balance     *big.Int   `protowire:"1"`
	Price       *big.Rat   `protowire:"2"`
	Coefficient *big.Float `protowire:"3"`
}

func TestBigNumRoundTrip(t *testing.T) {
	balance, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
	price := new(big.Rat).SetFrac64(31415, 10000) // 3.1415
	coeff := new(big.Float).SetPrec(128).SetFloat64(6.02214076e+23)

	orig := &BigNumStruct{
		Balance:     balance,
		Price:       price,
		Coefficient: coeff,
	}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &BigNumStruct{
		Balance:     new(big.Int),
		Price:       new(big.Rat),
		Coefficient: new(big.Float),
	}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if orig.Balance.Cmp(got.Balance) != 0 {
		t.Errorf("Balance mismatch: %s != %s", orig.Balance, got.Balance)
	}
	if orig.Price.Cmp(got.Price) != 0 {
		t.Errorf("Price mismatch: %s != %s", orig.Price, got.Price)
	}
	if orig.Coefficient.Cmp(got.Coefficient) != 0 {
		t.Errorf("Coefficient mismatch: %s != %s", orig.Coefficient.Text('g', 20), got.Coefficient.Text('g', 20))
	}
}

func TestBigNumNilPointers(t *testing.T) {
	orig := &BigNumStruct{} // all nil

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty bytes for nil big nums, got %d bytes", len(data))
	}
}

func TestBigNumNegative(t *testing.T) {
	orig := &BigNumStruct{
		Balance:     big.NewInt(-999999999999),
		Price:       new(big.Rat).SetFrac64(-1, 3), // non-terminating, capped at 50 digits
		Coefficient: new(big.Float).SetPrec(64).SetFloat64(-1.23e-45),
	}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &BigNumStruct{
		Balance:     new(big.Int),
		Price:       new(big.Rat),
		Coefficient: new(big.Float),
	}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if orig.Balance.Cmp(got.Balance) != 0 {
		t.Errorf("Balance mismatch: %s != %s", orig.Balance, got.Balance)
	}
	// For non-terminating decimal, we lose precision — just check sign
	if got.Price.Sign() >= 0 {
		t.Error("Price should be negative")
	}
	if orig.Coefficient.Cmp(got.Coefficient) != 0 {
		t.Errorf("Coefficient mismatch: %s != %s", orig.Coefficient.Text('g', 20), got.Coefficient.Text('g', 20))
	}
}

func TestBigNumPointerFields(t *testing.T) {
	// Test that pointer fields are allocated during unmarshal
	balance := big.NewInt(42)
	orig := &BigNumStruct{Balance: balance}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &BigNumStruct{
		Balance: new(big.Int),
	}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if balance.Cmp(got.Balance) != 0 {
		t.Errorf("Balance mismatch: %s != %s", balance, got.Balance)
	}
}

type WithZigZag struct {
	A int64 `protowire:"1"`        // proto3 int64 (plain varint)
	B int64 `protowire:"2,zigzag"` // proto3 sint64 (zigzag varint)
}

func TestZigZagTagOption(t *testing.T) {
	orig := &WithZigZag{A: -1, B: -1}
	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Field 1 (A=-1, plain varint): tag 0x08, then 10 bytes of 0xff..0x01.
	// Field 2 (B=-1, zigzag): tag 0x10, then varint(zigzag(-1)) = varint(1) = 0x01.
	// Total: 1 + 10 + 1 + 1 = 13 bytes; just check that B encodes as 1 byte after its tag.
	if len(data) != 13 {
		t.Fatalf("expected 13 bytes (sint64 -1 should be 1-byte zigzag, int64 -1 is 10 bytes), got %d: % x", len(data), data)
	}

	got := &WithZigZag{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.A != -1 || got.B != -1 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

type WithMaps struct {
	Tags    map[string]string `protowire:"1"`
	Codes   map[int32]string  `protowire:"2"`
	Counts  map[string]int64  `protowire:"3"`
	Servers map[string]Inner  `protowire:"4"`
}

func TestMapRoundTrip(t *testing.T) {
	orig := &WithMaps{
		Tags:   map[string]string{"env": "prod", "team": "platform", "key with space": "v"},
		Codes:  map[int32]string{404: "Not Found", 500: "Internal", -1: "Negative"},
		Counts: map[string]int64{"a": 1, "b": -7, "c": 0},
		Servers: map[string]Inner{
			"primary":   {Name: "p", Value: 1},
			"secondary": {Name: "s", Value: 2},
		},
	}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &WithMaps{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("map round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
}

func TestEmptyMap(t *testing.T) {
	// Empty (non-nil) and nil maps both produce empty bytes.
	for _, tc := range []struct {
		name string
		val  *WithMaps
	}{
		{"nil maps", &WithMaps{}},
		{"empty maps", &WithMaps{Tags: map[string]string{}, Codes: map[int32]string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Marshal(tc.val)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if len(data) != 0 {
				t.Errorf("expected empty bytes, got %d bytes", len(data))
			}
		})
	}
}

func TestMapZeroValueEntries(t *testing.T) {
	// proto3 map entries that contain zero-value keys/values still round-trip
	// — the entry message is emitted, but key/value fields are skipped per
	// proto3 semantics and decode back to zero.
	orig := &WithMaps{
		Tags:   map[string]string{"": "", "k": ""},
		Counts: map[string]int64{"": 0},
	}

	data, err := Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &WithMaps{}
	if err := Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Tags[""] != "" || got.Tags["k"] != "" {
		t.Errorf("Tags round-trip mismatch: got %+v", got.Tags)
	}
	if len(got.Tags) != 2 {
		t.Errorf("expected 2 Tags entries, got %d", len(got.Tags))
	}
	if got.Counts[""] != 0 {
		t.Errorf("Counts round-trip mismatch: got %+v", got.Counts)
	}
}
