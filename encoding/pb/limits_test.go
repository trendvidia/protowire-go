// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// decimalWire builds field 1 of a struct as a pxf.Decimal message with
// the given scale, by hand, so the decoder sees scales the encoder never
// writes. unscaled is the two bytes for 25.
func decimalWire(scale int32, negative bool) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, []byte{25})
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	// A plain int32 varint: sign-extended to 64 bits, as protobuf-go writes it.
	sub = protowire.AppendVarint(sub, uint64(int64(scale)))
	if negative {
		sub = protowire.AppendTag(sub, 3, protowire.VarintType)
		sub = protowire.AppendVarint(sub, 1)
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, sub)
}

type decimalHolder struct {
	D *big.Rat `protowire:"1"`
}

// promptly runs f and fails the test if it has not returned within the
// budget. A regression in a magnitude bound shows up as work proportional
// to the hostile value — on these inputs, minutes to never — so a plain
// call would hang the suite rather than fail it. The goroutine leaks on
// failure, which is fine for a test that has already failed.
func promptly(t *testing.T, budget time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		t.Fatalf("did not return within %v: the bound is not applied before the work it bounds", budget)
		return nil
	}
}

// TestDecimalScaleBound pins #95: a Decimal whose scale magnitude exceeds
// MaxNumericLiteralDigits is rejected before 10^|scale| is materialised,
// on both signs, and one at the bound still decodes to its exact value.
func TestDecimalScaleBound(t *testing.T) {
	pow := func(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }
	cases := []struct {
		name  string
		scale int32
		want  *big.Rat // nil: expect the bound's error
	}{
		{"at the bound", MaxNumericLiteralDigits, new(big.Rat).SetFrac(big.NewInt(25), pow(MaxNumericLiteralDigits))},
		{"at the negative bound", -MaxNumericLiteralDigits, new(big.Rat).SetInt(new(big.Int).Mul(big.NewInt(25), pow(MaxNumericLiteralDigits)))},
		{"one past the bound", MaxNumericLiteralDigits + 1, nil},
		{"one past the negative bound", -MaxNumericLiteralDigits - 1, nil},
		{"MaxInt32", math.MaxInt32, nil},
		{"MinInt32", math.MinInt32, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h decimalHolder
			err := promptly(t, 5*time.Second, func() error { return Unmarshal(decimalWire(tc.scale, false), &h) })
			if tc.want == nil {
				if err == nil {
					t.Fatalf("scale %d: decoded %s, want an error", tc.scale, h.D)
				}
				if !strings.Contains(err.Error(), "MaxNumericLiteralDigits") {
					t.Fatalf("scale %d: error %q does not name the limit", tc.scale, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("scale %d: %v", tc.scale, err)
			}
			if h.D.Cmp(tc.want) != 0 {
				t.Fatalf("scale %d: got %s, want %s", tc.scale, h.D.FloatString(8), tc.want.FloatString(8))
			}
		})
	}
}
