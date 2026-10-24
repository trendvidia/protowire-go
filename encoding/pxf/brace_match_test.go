// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import "testing"

// braceMatch finds the '}' that closes an @proto body with the rule
// Parse always used, whether it sees the body at once or a piece at a
// time, as the dataset header scan feeds it.
func FuzzBraceMatch(f *testing.F) {
	for _, s := range []string{
		"{}",
		"{ message M { string a = 1; } }",
		"{ x { } y }",
		`{ "}" }`,
		`{ "a\"}" }`,
		`{ "a\` + "\n" + `}" }`,
		`{ "a` + "\n" + `}" }`,
		`{ """ } "" } """ }`,
		`{ """"}"""" }`,
		`{ b"}" }`,
		`{ b"` + "\n" + `" }`,
		"{ # } \n }",
		"{ // } \n }",
		"{ /* } */ }",
		"{ /*/ } */ }",
		"{ /**/ }",
		"{ / }",
		`{ ab"}" }`,
		`{ ""`,
		`{ "`,
		"{ b",
		"{ /",
	} {
		f.Add(s, uint8(1))
	}
	f.Fuzz(func(t *testing.T, s string, step uint8) {
		in := []byte(s)
		open := -1
		for i, ch := range in {
			if ch == '{' {
				open = i
				break
			}
		}
		if open < 0 {
			return
		}
		want := oracleMatchingBrace(in, open)
		if got := findMatchingBrace(in, open); got != want {
			t.Fatalf("whole input: findMatchingBrace = %d, want %d", got, want)
		}
		// Fed a growing prefix, step bytes at a time, as the header scan
		// feeds it: it must stop where the whole input closes or fails,
		// and nowhere sooner.
		n := max(int(step), 1)
		m := braceMatch{depth: 1}
		i, st := open+1, braceOpen
		for end := min(open+1+n, len(in)); ; end = min(end+n, len(in)) {
			i, st = m.scan(in[:end], i)
			if st != braceOpen || end == len(in) {
				break
			}
		}
		got := -1
		if st == braceClosed {
			got = i
		}
		if got != want {
			t.Fatalf("fed %d bytes at a time: closed at %d (state %d), want %d", n, got, st, want)
		}
	})
}

// oracleMatchingBrace is findMatchingBrace as it was before braceMatch
// replaced it, kept as the rule the resumable scan must reproduce.
func oracleMatchingBrace(input []byte, openOffset int) int {
	depth := 1
	i := openOffset + 1
	for i < len(input) {
		ch := input[i]
		switch {
		case ch == '{':
			depth++
			i++
		case ch == '}':
			depth--
			if depth == 0 {
				return i
			}
			i++
		case ch == '"':
			i = skipDirString(input, i)
			if i < 0 {
				return -1
			}
		case ch == 'b' && i+1 < len(input) && input[i+1] == '"':
			i = oracleSkipBytes(input, i)
			if i < 0 {
				return -1
			}
		case ch == '#':
			i = oracleSkipEOL(input, i+1)
		case ch == '/' && i+1 < len(input) && input[i+1] == '/':
			i = oracleSkipEOL(input, i+2)
		case ch == '/' && i+1 < len(input) && input[i+1] == '*':
			j := i + 2
			closed := false
			for j+1 < len(input) {
				if input[j] == '*' && input[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				return -1
			}
			i = j
		default:
			i++
		}
	}
	return -1
}

func oracleSkipBytes(input []byte, i int) int {
	j := i + 2 // past `b"`
	for j < len(input) {
		if input[j] == '"' {
			return j + 1
		}
		if input[j] == '\n' {
			return -1
		}
		j++
	}
	return -1
}

func oracleSkipEOL(input []byte, i int) int {
	for i < len(input) && input[i] != '\n' {
		i++
	}
	return i
}
