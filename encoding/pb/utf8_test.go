// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

type utf8Inner struct {
	Text string `protowire:"1"`
}

type utf8Named string

type utf8Msg struct {
	Name   string            `protowire:"1"`
	Ptr    *string           `protowire:"2"`
	Named  utf8Named         `protowire:"3"`
	List   []string          `protowire:"4"`
	Inner  utf8Inner         `protowire:"5"`
	Labels map[string]string `protowire:"6"`
	Data   []byte            `protowire:"7"`
}

func lenField(b []byte, num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), v)
}

// HARDENING.md § UTF-8 names PB length-delimited bytes. Each place a string can
// sit, and each way a byte sequence can be invalid.
func TestStringFieldRejectsInvalidUTF8(t *testing.T) {
	entry := func(key, value string) []byte {
		return lenField(lenField(nil, 1, []byte(key)), 2, []byte(value))
	}
	sites := []struct {
		name  string
		wire  func(bad string) []byte
		wantE string
	}{
		{"singular", func(s string) []byte { return lenField(nil, 1, []byte(s)) }, "field Name: invalid UTF-8 in string"},
		{"pointer", func(s string) []byte { return lenField(nil, 2, []byte(s)) }, "field Ptr: invalid UTF-8 in string"},
		{"named type", func(s string) []byte { return lenField(nil, 3, []byte(s)) }, "field Named: invalid UTF-8 in string"},
		{"list element", func(s string) []byte {
			return lenField(lenField(nil, 4, []byte("ok")), 4, []byte(s))
		}, "field List: invalid UTF-8 in string"},
		{"nested", func(s string) []byte {
			return lenField(nil, 5, lenField(nil, 1, []byte(s)))
		}, "field Inner: field Text: invalid UTF-8 in string"},
		{"map key", func(s string) []byte { return lenField(nil, 6, entry(s, "v")) }, "field Labels: map key: invalid UTF-8 in string"},
		{"map value", func(s string) []byte { return lenField(nil, 6, entry("k", s)) }, "field Labels: map value: invalid UTF-8 in string"},
	}
	sequences := []struct{ name, bad string }{
		{"stray byte", "AB\xff"},
		{"lone continuation", "\x80"},
		{"latin-1", "caf\xe9"},
		{"cut off", "abc\xc3"},
		{"overlong", "\xc0\x80"},
		{"surrogate", "\xed\xa0\x80"},
		{"past U+10FFFF", "\xf4\x90\x80\x80"},
	}
	for _, site := range sites {
		for _, seq := range sequences {
			t.Run(site.name+"/"+seq.name, func(t *testing.T) {
				wire := site.wire(seq.bad)
				if err := Unmarshal(wire, &utf8Msg{}); err == nil || err.Error() != site.wantE {
					t.Errorf("Unmarshal = %v, want %q", err, site.wantE)
				}

				framed := protowire.AppendBytes(nil, wire)
				if err := NewDecoder(bytes.NewReader(framed)).Decode(&utf8Msg{}); err == nil || err.Error() != site.wantE {
					t.Errorf("Decode = %v, want %q", err, site.wantE)
				}
			})
		}
	}
}

// The check must not refuse what the rule allows.
func TestStringFieldAcceptsValidUTF8(t *testing.T) {
	ptr := "€"
	want := utf8Msg{
		Name:   "é",
		Ptr:    &ptr,
		Named:  "😀",
		List:   []string{"a\x00b", ""},
		Inner:  utf8Inner{Text: "日本語"},
		Labels: map[string]string{"ключ": "значение"},
		Data:   []byte{0xff, 0xfe, 0x80, 0xc0},
	}
	wire, err := Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	var got utf8Msg
	if err := Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// Bad text is a bad message, not a bad stream.
func TestDecoderContinuesPastInvalidUTF8(t *testing.T) {
	var stream []byte
	for _, text := range []string{"AB\xff", "good"} {
		stream = protowire.AppendBytes(stream, lenField(nil, 1, []byte(text)))
	}
	dec := NewDecoder(bytes.NewReader(stream))

	if err := dec.Decode(&utf8Msg{}); err == nil || err.Error() != "field Name: invalid UTF-8 in string" {
		t.Fatalf("first frame = %v", err)
	}
	var next utf8Msg
	if err := dec.Decode(&next); err != nil || next.Name != "good" {
		t.Fatalf("second frame = %+v, %v", next, err)
	}
	if err := dec.Decode(&utf8Msg{}); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream = %v", err)
	}
}
