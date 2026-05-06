// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// sofhEncodingSBE_LE is the FIX SOFH-registered encoding type for
// "Simple Binary Encoding Version 1.0 with Little-Endian byte order".
// The codec writes LE (sbe.go:64), so receivers must reject any other value.
const sofhEncodingSBE_LE uint16 = 0xEB50

const sofhHeaderLen = 6

// DefaultMaxFrameSize caps the SBE body size at 16 MiB by default.
const DefaultMaxFrameSize = 16 << 20

// ErrFramingCorrupt is returned when the SOFH framing is invalid (wrong
// encoding type, length less than the header, frame larger than
// MaxFrameSize, etc.). The byte stream is desynchronized after this error
// and the decoder is permanently unusable.
var ErrFramingCorrupt = errors.New("sbe: stream framing corrupt")

// Encoder writes SBE messages framed with a Simple Open Framing Header.
type Encoder struct {
	codec *Codec
	w     io.Writer
	hdr   [sofhHeaderLen]byte
}

func (c *Codec) NewEncoder(w io.Writer) *Encoder { return &Encoder{codec: c, w: w} }

func (e *Encoder) Encode(msg proto.Message) error {
	body, err := e.codec.Marshal(msg)
	if err != nil {
		return err
	}
	total := uint32(sofhHeaderLen + len(body))
	binary.BigEndian.PutUint32(e.hdr[0:4], total)
	binary.BigEndian.PutUint16(e.hdr[4:6], sofhEncodingSBE_LE)
	if _, err := e.w.Write(e.hdr[:]); err != nil {
		return err
	}
	_, err = e.w.Write(body)
	return err
}

// Decoder reads SOFH-framed SBE messages. The internal scratch buffer is
// reused across Decode calls; Codec.Unmarshal copies parsed values into the
// destination message, so the buffer is the decoder's to overwrite.
type Decoder struct {
	codec *Codec
	r     io.Reader
	hdr   [sofhHeaderLen]byte
	buf   []byte
	max   int
	err   error
}

func (c *Codec) NewDecoder(r io.Reader) *Decoder {
	return &Decoder{codec: c, r: r, max: DefaultMaxFrameSize}
}

func (d *Decoder) SetMaxFrameSize(n int) { d.max = n }

func (d *Decoder) Decode(msg proto.Message) error {
	if d.err != nil {
		return d.err
	}
	n, err := io.ReadFull(d.r, d.hdr[:])
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return io.EOF
		}
		d.err = io.ErrUnexpectedEOF
		return io.ErrUnexpectedEOF
	}
	total := binary.BigEndian.Uint32(d.hdr[0:4])
	enc := binary.BigEndian.Uint16(d.hdr[4:6])
	if enc != sofhEncodingSBE_LE {
		d.err = fmt.Errorf("%w: SOFH encoding type 0x%04x not supported", ErrFramingCorrupt, enc)
		return d.err
	}
	if total < sofhHeaderLen {
		d.err = fmt.Errorf("%w: SOFH total length %d < header size", ErrFramingCorrupt, total)
		return d.err
	}
	bodyLen := int(total) - sofhHeaderLen
	if bodyLen > d.max {
		d.err = fmt.Errorf("%w: frame size %d exceeds max %d", ErrFramingCorrupt, bodyLen, d.max)
		return d.err
	}
	if cap(d.buf) < bodyLen {
		d.buf = make([]byte, bodyLen)
	} else {
		d.buf = d.buf[:bodyLen]
	}
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		d.err = err
		return err
	}
	return d.codec.Unmarshal(d.buf, msg)
}
