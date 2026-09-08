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

	// The draft's per-call limits (draft -01 § Mandatory Limits: every
	// limit but MaxVarintBytes is "configurable per call by the calling
	// application"). Zero means the package constant of the same name,
	// so the zero value of UnmarshalOptions is unchanged.
	//
	// MaxMessageSize caps the input to this call and is checked before
	// anything is read; MaxNestingDepth caps submessage / map-entry
	// recursion; MaxNumericLiteralDigits bounds the magnitude of a
	// pxf.Decimal's scale; MaxRepeatedCount caps the element count of
	// any repeated or map field.
	MaxMessageSize          int
	MaxNestingDepth         int
	MaxNumericLiteralDigits int
	MaxRepeatedCount        int
}

// limits resolves the per-call limits, the package constants standing in
// for zero.
func (o UnmarshalOptions) limits() limits {
	lim := defaultLimits
	if o.MaxMessageSize > 0 {
		lim.maxMessageSize = o.MaxMessageSize
	}
	if o.MaxNestingDepth > 0 {
		lim.maxDepth = o.MaxNestingDepth
	}
	if o.MaxNumericLiteralDigits > 0 {
		lim.maxDigits = o.MaxNumericLiteralDigits
	}
	if o.MaxRepeatedCount > 0 {
		lim.maxRepeated = o.MaxRepeatedCount
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
