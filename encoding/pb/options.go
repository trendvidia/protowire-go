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
}

// Unmarshal decodes protobuf binary into a struct, then applies the
// configured Validator. v must be a pointer to a struct with
// protowire:"N" tags.
func (o UnmarshalOptions) Unmarshal(data []byte, v any) error {
	if err := Unmarshal(data, v); err != nil {
		return err
	}
	_, err := check.Validate(o.Validator, v)
	return err
}
