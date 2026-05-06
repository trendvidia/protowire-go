// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package envelope

import (
	"io"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// Encoder writes length-prefixed Envelopes using the protobuf-delimited
// framing convention (varint length + body), reusing pb.Encoder so framing
// has one bug-fix site.
type Encoder struct {
	*pb.Encoder
}

func NewEncoder(w io.Writer) *Encoder { return &Encoder{pb.NewEncoder(w)} }

func (e *Encoder) Encode(env *Envelope) error { return e.Encoder.Encode(env) }

// Decoder reads length-prefixed Envelopes. SetMaxFrameSize and the
// ErrFramingCorrupt sentinel are inherited from the embedded *pb.Decoder.
type Decoder struct {
	*pb.Decoder
}

func NewDecoder(r io.Reader) *Decoder { return &Decoder{pb.NewDecoder(r)} }

func (d *Decoder) Decode(env *Envelope) error { return d.Decoder.Decode(env) }
