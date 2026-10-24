// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Dotted column paths (protowire#342, draft -01 §3.4.4). A column entry
// may be a path such as "descriptor.energy": a non-empty cell in it
// stands for "energy = <value>" inside the block "descriptor { ... }",
// and every cell of a row that shares a path prefix lands in the same
// one block.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// columnPathError checks the header rules that need no schema: no path
// has an empty segment, no path appears twice, and no column is a prefix
// of another ("a" beside "a.b"), since a row could then set the same
// sub-message twice. It returns the index of the first column that
// breaks one, with the message, or -1.
func columnPathError(cols []string) (int, string) {
	for i, c := range cols {
		if strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".") || strings.Contains(c, "..") {
			return i, fmt.Sprintf("@dataset column %q: a column path has an empty segment (draft §3.4.4)", c)
		}
		for _, prev := range cols[:i] {
			switch {
			case prev == c:
				return i, fmt.Sprintf("@dataset column %q appears twice (draft §3.4.4)", c)
			case isPathPrefix(prev, c), isPathPrefix(c, prev):
				return i, fmt.Sprintf("@dataset column %q overlaps column %q: a field and a path inside it cannot both be columns (draft §3.4.4)", c, prev)
			}
		}
	}
	return -1, ""
}

// isPathPrefix reports whether path goes inside the field prefix names:
// "a" is a path prefix of "a.b", and not of "ab".
func isPathPrefix(prefix, path string) bool {
	return len(path) > len(prefix) && path[len(prefix)] == '.' && strings.HasPrefix(path, prefix)
}

// checkColumnPaths checks every column against the row type desc, and
// returns the message for the first that breaks a rule, or "". Each
// segment names a field, and each segment but the last names a singular
// message field whose type is not written as a literal of its own (a
// Timestamp, Duration, wrapper, pxf.BigInt, pxf.Decimal or
// pxf.BigFloat). A path has no index, so it cannot go through a repeated
// or map field. A pxf.Secret has a block form as well as its literal
// (`pw { value = "x" hint = "h" }`), so a path may go into one. A plain
// column is a path of one segment, so naming no field of desc is a
// header error for it too, whether its cells are empty or not (draft
// §3.4.4: each column entry names a field declared on the type). A
// header that passes allocates nothing, since [BindRow] checks it on
// every row.
func checkColumnPaths(desc protoreflect.MessageDescriptor, cols []string) string {
	if i, msg := columnPathError(cols); i >= 0 {
		return msg
	}
	for _, c := range cols {
		md := desc
		for rest := c; ; {
			seg, tail, more := strings.Cut(rest, ".")
			fd := md.Fields().ByName(protoreflect.Name(seg))
			if fd == nil {
				return fmt.Sprintf("@dataset column %q: %s has no field %q", c, md.FullName(), seg)
			}
			if !more {
				break
			}
			at := c[:len(c)-len(tail)-1]
			switch {
			case fd.IsMap():
				return fmt.Sprintf("@dataset column %q: %q is a map field, and a column path cannot go through one; use a value cell for %q (draft §3.4.4)", c, at, at)
			case fd.IsList():
				return fmt.Sprintf("@dataset column %q: %q is a repeated field, and a column path cannot go through one; use a value cell for %q (draft §3.4.4)", c, at, at)
			case fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind:
				return fmt.Sprintf("@dataset column %q: %q is not a message field", c, at)
			case isLiteralMessageType(fd.Message()) && !isSecret(fd.Message()):
				return fmt.Sprintf("@dataset column %q: %q is a %s, which is written as a literal; a column path cannot go into it (draft §3.4.4)", c, at, fd.Message().FullName())
			}
			md = fd.Message()
			rest = tail
		}
	}
	return ""
}
