// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import "testing"

// TestLexesAsBareMapKey pins the safety net for a MapEntry built in code
// (#123): the key is written bare only when the lexer reads the text back
// as exactly one identifier, integer or bool token spelled that way.
func TestLexesAsBareMapKey(t *testing.T) {
	for s, want := range map[string]bool{
		"plain": true, "a.b": true, "_x1": true, "404": true, "-5": true, "0": true,
		"true": true, "false": true,
		"": false, "null": false, "my key": false, " a": false, "a ": false, "a\nb": false,
		"1.5": false, "5ms": false, "2024-01-01": false, "+1": false, "\"q\"": false,
		"#c": false, "-": false, "a:b": false,
	} {
		if got := lexesAsBareMapKey(s); got != want {
			t.Errorf("lexesAsBareMapKey(%q) = %v, want %v", s, got, want)
		}
	}
}
