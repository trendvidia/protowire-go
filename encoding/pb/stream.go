// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxFrameSize caps each frame at 16 MiB by default. Streams face
// per-connection adversary risk; callers can adjust via SetMaxFrameSize.
const DefaultMaxFrameSize = 16 << 20

// ErrFramingCorrupt is returned when the length-prefix framing is invalid
// (corrupt varint, frame larger than MaxFrameSize, etc.). After this error
// the byte stream is desynchronized and the decoder is permanently unusable.
var ErrFramingCorrupt = errors.New("pb: stream framing corrupt")

// Encoder writes length-prefixed pb-encoded messages using the
// protobuf-delimited framing convention (varint length + body).
type Encoder struct {
	w   io.Writer
	hdr [binary.MaxVarintLen64]byte
}

func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

func (e *Encoder) Encode(v any) error {
	body, err := Marshal(v)
	if err != nil {
		return err
	}
	n := binary.PutUvarint(e.hdr[:], uint64(len(body)))
	if _, err := e.w.Write(e.hdr[:n]); err != nil {
		return err
	}
	_, err = e.w.Write(body)
	return err
}

type byteReader interface {
	io.Reader
	io.ByteReader
}

// Decoder reads length-prefixed pb-encoded messages. The internal scratch
// buffer is reused across Decode calls; Unmarshal copies parsed values into
// v, so the buffer is the decoder's to overwrite on the next call.
type Decoder struct {
	r   byteReader
	buf []byte
	max int
	err error
}

func NewDecoder(r io.Reader) *Decoder {
	d := &Decoder{max: DefaultMaxFrameSize}
	if br, ok := r.(byteReader); ok {
		d.r = br
	} else {
		d.r = bufio.NewReader(r)
	}
	return d
}

func (d *Decoder) SetMaxFrameSize(n int) { d.max = n }

func (d *Decoder) Decode(v any) error {
	if d.err != nil {
		return d.err
	}
	n, err := binary.ReadUvarint(d.r)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			d.err = io.ErrUnexpectedEOF
			return io.ErrUnexpectedEOF
		}
		d.err = fmt.Errorf("%w: %v", ErrFramingCorrupt, err)
		return d.err
	}
	if n > uint64(d.max) {
		d.err = fmt.Errorf("%w: frame size %d exceeds max %d", ErrFramingCorrupt, n, d.max)
		return d.err
	}
	if cap(d.buf) < int(n) {
		d.buf = make([]byte, n)
	} else {
		d.buf = d.buf[:n]
	}
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		d.err = err
		return err
	}
	return Unmarshal(d.buf, v)
}
