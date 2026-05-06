// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type StreamMsg struct {
	N    int    `protowire:"1"`
	Text string `protowire:"2"`
	Data []byte `protowire:"3"`
}

func TestStreamRoundTrip(t *testing.T) {
	msgs := []*StreamMsg{
		{N: 1, Text: "small"},
		{N: 2, Text: strings.Repeat("medium ", 128)},
		{N: 3, Data: bytes.Repeat([]byte{0xab}, 1<<20)},
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for i, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatalf("encode[%d]: %v", i, err)
		}
	}

	dec := NewDecoder(&buf)
	for i, want := range msgs {
		got := &StreamMsg{}
		if err := dec.Decode(got); err != nil {
			t.Fatalf("decode[%d]: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("frame %d mismatch", i)
		}
	}
	if err := dec.Decode(&StreamMsg{}); err != io.EOF {
		t.Errorf("after last frame: got %v, want io.EOF", err)
	}
}

func TestStreamCleanEOF(t *testing.T) {
	dec := NewDecoder(&bytes.Buffer{})
	if err := dec.Decode(&StreamMsg{}); err != io.EOF {
		t.Errorf("empty stream: got %v, want io.EOF", err)
	}
}

func TestStreamPartialPrefix(t *testing.T) {
	dec := NewDecoder(bytes.NewReader([]byte{0x80}))
	err := dec.Decode(&StreamMsg{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
	if err2 := dec.Decode(&StreamMsg{}); err2 != err {
		t.Errorf("not sticky: %v vs %v", err, err2)
	}
}

func TestStreamPartialBody(t *testing.T) {
	var buf bytes.Buffer
	hdr := [binary.MaxVarintLen64]byte{}
	n := binary.PutUvarint(hdr[:], 100)
	buf.Write(hdr[:n])
	buf.Write(make([]byte, 10))
	dec := NewDecoder(&buf)
	err := dec.Decode(&StreamMsg{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
	if err2 := dec.Decode(&StreamMsg{}); err2 != err {
		t.Errorf("not sticky")
	}
}

func TestStreamOversized(t *testing.T) {
	var buf bytes.Buffer
	hdr := [binary.MaxVarintLen64]byte{}
	n := binary.PutUvarint(hdr[:], 1<<30)
	buf.Write(hdr[:n])
	dec := NewDecoder(&buf)
	dec.SetMaxFrameSize(1 << 20)
	err := dec.Decode(&StreamMsg{})
	if !errors.Is(err, ErrFramingCorrupt) {
		t.Fatalf("got %v, want ErrFramingCorrupt", err)
	}
	if err2 := dec.Decode(&StreamMsg{}); !errors.Is(err2, ErrFramingCorrupt) {
		t.Errorf("not sticky")
	}
}

func TestStreamVarintOverflow(t *testing.T) {
	junk := bytes.Repeat([]byte{0xff}, 11)
	dec := NewDecoder(bytes.NewReader(junk))
	err := dec.Decode(&StreamMsg{})
	if !errors.Is(err, ErrFramingCorrupt) {
		t.Errorf("got %v, want ErrFramingCorrupt", err)
	}
}

func TestStreamInnerUnmarshalRecoverable(t *testing.T) {
	// A frame with a corrupt body shouldn't desync the framing — the next
	// frame is at a known boundary, so the caller can keep going.
	good := &StreamMsg{N: 7, Text: "ok"}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(good); err != nil {
		t.Fatal(err)
	}
	hdr := [binary.MaxVarintLen64]byte{}
	n := binary.PutUvarint(hdr[:], 4)
	buf.Write(hdr[:n])
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff})
	if err := enc.Encode(good); err != nil {
		t.Fatal(err)
	}

	dec := NewDecoder(&buf)
	got := &StreamMsg{}
	if err := dec.Decode(got); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	err := dec.Decode(&StreamMsg{})
	if err == nil {
		t.Fatalf("frame 2: expected error")
	}
	if errors.Is(err, ErrFramingCorrupt) {
		t.Fatalf("frame 2: got framing error, expected inner unmarshal: %v", err)
	}
	got3 := &StreamMsg{}
	if err := dec.Decode(got3); err != nil {
		t.Fatalf("frame 3 after recoverable error: %v", err)
	}
	if got3.N != 7 || got3.Text != "ok" {
		t.Errorf("frame 3 mismatch: %+v", got3)
	}
}

func TestStreamScratchReuse(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for range 3 {
		if err := enc.Encode(&StreamMsg{Text: strings.Repeat("x", 100)}); err != nil {
			t.Fatal(err)
		}
	}
	dec := NewDecoder(&buf)
	if err := dec.Decode(&StreamMsg{}); err != nil {
		t.Fatal(err)
	}
	addr1 := &dec.buf[0]
	if err := dec.Decode(&StreamMsg{}); err != nil {
		t.Fatal(err)
	}
	addr2 := &dec.buf[0]
	if addr1 != addr2 {
		t.Errorf("scratch buffer reallocated between same-size decodes")
	}
}

func TestStreamLengthBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, 127, 128, 16383, 16384, 1 << 17} {
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		msg := &StreamMsg{Data: bytes.Repeat([]byte{0xcc}, size)}
		if err := enc.Encode(msg); err != nil {
			t.Fatalf("size=%d encode: %v", size, err)
		}
		dec := NewDecoder(&buf)
		got := &StreamMsg{}
		if err := dec.Decode(got); err != nil {
			t.Fatalf("size=%d decode: %v", size, err)
		}
		if !bytes.Equal(got.Data, msg.Data) {
			t.Errorf("size=%d data mismatch", size)
		}
	}
}

func FuzzStreamDecode(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0x03, 0x08, 0x2a, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		dec := NewDecoder(bytes.NewReader(data))
		dec.SetMaxFrameSize(1 << 20)
		for range 64 {
			if err := dec.Decode(&StreamMsg{}); err != nil {
				return
			}
		}
	})
}
