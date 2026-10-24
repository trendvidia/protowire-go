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
	} {
		pr, pw := io.Pipe()
		go func() { _, _ = pw.Write([]byte("@dataset T (a)\n" + row)) }()

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
