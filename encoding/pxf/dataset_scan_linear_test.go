// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// The row scan lexes each byte of a row about once, however many reads
// the row arrives in (#148), and never holds back a row that is complete
// (the liveness half of the same design, #149).
//
// These count the bytes the scan lexes (DatasetReader.scanned) instead
// of timing it, so a slow or loaded machine cannot fail them; before
// #148 an unclosed row of n bytes cost about n²/8192 byte visits, which
// is the 4.79s the issue measured for 4 MiB.

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestDatasetReader_ScanIsLinear(t *testing.T) {
	shapes := []struct {
		name string
		body func(n int) string
	}{
		// The issue's shape: many short tokens, no closing ')'.
		{"short tokens", func(n int) string { return strings.Repeat("1 ", n/2) }},
		// One token that never ends: each read used to re-lex it whole.
		{"unterminated string", func(n int) string { return `"` + strings.Repeat("a", n) }},
		{"string of escaped quotes", func(n int) string { return `"` + strings.Repeat(`a\"`, n/3) }},
		{"unterminated triple string", func(n int) string { return `"""` + strings.Repeat(`a""b`, n/4) }},
		{"one long number", func(n int) string { return strings.Repeat("7", n) }},
		{"line comment", func(n int) string { return "1 # " + strings.Repeat("x", n) }},
		{"unterminated block comment", func(n int) string { return "1 /* " + strings.Repeat("/ * x", n/5) }},
		// A timestamp runs to a byte that ends a run; '=' and '/' are
		// not one, and must not make the scan lex it again.
		{"one long timestamp", func(n int) string { return "2026-10-23T" + strings.Repeat("0=/", n/3) }},
		{"one long identifier", func(n int) string { return "a" + strings.Repeat("b.", n/2) }},
	}
	// The issue's sizes, 4 and 8 MiB. Under -race, 256 and 512 KiB: the
	// count is what is checked, and a quadratic scan already lexes about
	// 8M bytes for a 256 KiB row in 4 KiB reads, far over the 2x limit.
	sizes := []int{4 << 20, 8 << 20}
	if raceEnabled {
		sizes = []int{256 << 10, 512 << 10}
	}
	for _, sh := range shapes {
		for _, size := range sizes {
			for _, oneByte := range []bool{false, true} {
				in := "@dataset T (a)\n(" + sh.body(size)
				var r io.Reader = strings.NewReader(in)
				if oneByte {
					r = iotest.OneByteReader(r)
				}
				tr, err := NewDatasetReader(r)
				if err != nil {
					t.Fatalf("%s: %v", sh.name, err)
				}
				tr.scanned = 0 // the row, not the header
				if _, err := tr.Next(); err == nil {
					t.Fatalf("%s: an unclosed row was accepted", sh.name)
				}
				// A token cut by a read is lexed again once; short
				// tokens in one-byte reads come to about 1.5x.
				if limit := 2 * len(in); tr.scanned > limit {
					t.Errorf("%s, %d bytes, one-byte reads %v: scanned %d bytes of %d (limit %d)",
						sh.name, size, oneByte, tr.scanned, len(in), limit)
				}
			}
		}
	}
}

// The header scan resumes too: a header near the 64 KiB budget, in
// one-byte reads, used to be lexed from its start on every read, about
// 1.8G byte visits for 60 KB.
func TestDatasetReader_HeaderScanIsLinear(t *testing.T) {
	const n = 60 << 10
	for _, sh := range []struct{ name, header string }{
		{"short tokens", "@x { " + strings.Repeat("a = 1 ", n/6) + "}\n"},
		{"one long string", `@x { k = "` + strings.Repeat("a", n) + "\" }\n"},
		{"@proto body", "@proto { " + strings.Repeat("string a = 1; ", n/14) + "}\n"},
	} {
		in := sh.header + "@dataset T (a)\n(1)\n"
		tr, err := NewDatasetReader(iotest.OneByteReader(strings.NewReader(in)))
		if err != nil {
			t.Fatalf("%s: %v", sh.name, err)
		}
		if limit := 2 * len(in); tr.scanned > limit {
			t.Errorf("%s: scanned %d bytes of %d (limit %d)", sh.name, tr.scanned, len(in), limit)
		}
	}
}

// A row whose ')' is the last byte read is returned at once: the scan
// must not wait for bytes that may never come (a live, append-only
// stream). Each case writes a header and one complete row into a pipe
// and leaves the pipe open.
func TestDatasetReader_CompleteRowIsNotHeldBack(t *testing.T) {
	for _, row := range []string{
		`(1)`,
		`("")`,
		`("a\"b")`,
		`(abc)`,
		`(2026-10-23T00:00:00Z)`,
		`(5s)`,
		`(-1.5e3)`,
		`(b"QQ==")`,
		`({ k = [1, 2] })`,
		"(\"\"\"\nx\n\"\"\")",
		`(1 /* c */)`,
		`(1h.5m)`,
		"(1\xc2\xb5s)",
		`(-inf)`,
		`("\x41\U0001F600")`,
		`(aaaaaaaaaaaa)`,
	} {
		// One write per byte, so every byte of the row ends a read.
		pr, pw := io.Pipe()
		go func() {
			for _, b := range []byte("@dataset T (a)\n" + row) {
				if _, err := pw.Write([]byte{b}); err != nil {
					return
				}
			}
		}()

		got := make(chan error, 1)
		go func() {
			tr, err := NewDatasetReader(pr)
			if err == nil {
				_, err = tr.Next()
			}
			got <- err
		}()
		select {
		case err := <-got:
			if err != nil {
				t.Errorf("%s: %v", row, err)
			}
		case <-time.After(10 * time.Second):
			// Generous: the row is a few bytes. Reaching this means the
			// reader is blocked in Read waiting for more input.
			t.Errorf("%s: the complete row was not returned while the stream stayed open", row)
		}
		_ = pw.Close()
	}
}

// A row the bytes read so far already show to be malformed fails at
// once, with Parse's error for those bytes, while the stream stays open:
// a token the lexer has rejected is not held back for input that cannot
// change it.
func TestDatasetReader_DecidedErrorIsNotHeldBack(t *testing.T) {
	opts := UnmarshalOptions{MaxBytesLiteralLength: 4}
	for _, writes := range [][]string{
		{"(~"},
		{"(1, ", "~"},
		{"(aaaaaaaaaaaa", "~"},
		{"(1h", ".", "~"},
		// The cap refuses a bytes literal before its closing quote.
		{`(b"`, "QUFBQUFBQUFBQUFBQUFB"},
		{`(b"QUFB`, "QUFBQUFBQUFBQUFB"},
	} {
		in := "@dataset T (a)\n" + strings.Join(writes, "")
		_, perr := ParseOptions{MaxBytesLiteralLength: 4}.Parse([]byte(in))
		if perr == nil {
			t.Fatalf("%q: Parse accepted it", writes)
		}
		pr, pw := io.Pipe()
		go func() {
			if _, err := pw.Write([]byte("@dataset T (a)\n")); err != nil {
				return
			}
			for _, w := range writes {
				if _, err := pw.Write([]byte(w)); err != nil {
					return
				}
			}
		}()
		got := make(chan error, 1)
		go func() {
			tr, err := opts.NewDatasetReader(pr)
			if err == nil {
				_, err = tr.Next()
			}
			got <- err
		}()
		select {
		case err := <-got:
			if err == nil || err.Error() != perr.Error() {
				t.Errorf("%q: got %v, want Parse's %v", writes, err, perr)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%q: the malformed row was not reported while the stream stayed open", writes)
		}
		_ = pw.Close()
	}
}
