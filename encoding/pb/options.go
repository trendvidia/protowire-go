// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import "github.com/trendvidia/protowire-go/check"

// UnmarshalOptions configures PB decoding. The zero value is equivalent
// to the package-level [Unmarshal].
type UnmarshalOptions struct {
	// Validator, if non-nil, runs data validation after a successful
	// decode. It receives the same *struct pointer passed to Unmarshal
	// — a protowire-tagged native Go struct, not a proto.Message — so
	// engines that only understand descriptor-backed messages must be
	// paired with an adapter that knows this struct's schema. When the
	// validator reports violations, Unmarshal fails with a
	// *check.Error, retrievable via errors.As.
	Validator check.Validator

	// MaxNestingDepth caps submessage / map-entry recursion for this
	// call, and MaxNumericLiteralDigits the magnitude of a pxf.Decimal's
	// scale (draft -01 § Mandatory Limits: every limit but
	// MaxVarintBytes is "configurable per call by the calling
	// application"). Zero means the package constant of the same name,
	// so the zero value of UnmarshalOptions is unchanged.
	MaxNestingDepth         int
	MaxNumericLiteralDigits int
}

// limits resolves the per-call limits, the package constants standing in
// for zero.
func (o UnmarshalOptions) limits() limits {
	lim := defaultLimits
	if o.MaxNestingDepth > 0 {
		lim.maxDepth = o.MaxNestingDepth
	}
	if o.MaxNumericLiteralDigits > 0 {
		lim.maxDigits = o.MaxNumericLiteralDigits
	}
	return lim
}

// Unmarshal decodes protobuf binary into a struct under the options'
// limits, then applies the configured Validator. v must be a pointer to
// a struct with protowire:"N" tags.
func (o UnmarshalOptions) Unmarshal(data []byte, v any) error {
	if err := unmarshal(data, v, o.limits()); err != nil {
		return err
	}
	_, err := check.Validate(o.Validator, v)
	return err
}
