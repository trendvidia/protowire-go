// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package envelope

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

func TestStreamRoundTrip(t *testing.T) {
	envs := []*Envelope{
		OK(200, []byte{0xDE, 0xAD, 0xBE, 0xEF}),
		Err(400, "INVALID_ARG", "bad input", "field=name").
			withDetails(),
		TransportErr("connection refused"),
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for i, e := range envs {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode[%d]: %v", i, err)
		}
	}

	dec := NewDecoder(&buf)
	for i, want := range envs {
		got := &Envelope{}
		if err := dec.Decode(got); err != nil {
			t.Fatalf("decode[%d]: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("frame %d mismatch:\n  want %+v\n  got  %+v", i, want, got)
		}
	}
	if err := dec.Decode(&Envelope{}); err != io.EOF {
		t.Errorf("after last frame: got %v, want io.EOF", err)
	}
}

func (e *Envelope) withDetails() *Envelope {
	e.Error.WithField("name", "REQUIRED", "name is required").
		WithMeta("trace_id", "abc123")
	return e
}

func TestStreamCleanEOF(t *testing.T) {
	dec := NewDecoder(&bytes.Buffer{})
	if err := dec.Decode(&Envelope{}); err != io.EOF {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestStreamPartialBody(t *testing.T) {
	var buf bytes.Buffer
	hdr := [binary.MaxVarintLen64]byte{}
	n := binary.PutUvarint(hdr[:], 50)
	buf.Write(hdr[:n])
	buf.Write(make([]byte, 5))
	dec := NewDecoder(&buf)
	if err := dec.Decode(&Envelope{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestStreamOversized(t *testing.T) {
	var buf bytes.Buffer
	hdr := [binary.MaxVarintLen64]byte{}
	n := binary.PutUvarint(hdr[:], 1<<30)
	buf.Write(hdr[:n])
	dec := NewDecoder(&buf)
	dec.SetMaxFrameSize(1 << 16)
	if err := dec.Decode(&Envelope{}); !errors.Is(err, pb.ErrFramingCorrupt) {
		t.Errorf("got %v, want ErrFramingCorrupt", err)
	}
}

func FuzzStreamDecode(f *testing.F) {
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		dec := NewDecoder(bytes.NewReader(data))
		dec.SetMaxFrameSize(1 << 20)
		for range 64 {
			if err := dec.Decode(&Envelope{}); err != nil {
				return
			}
		}
	})
}
