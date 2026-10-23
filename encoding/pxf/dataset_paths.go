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
			case strings.HasPrefix(c, prev+"."), strings.HasPrefix(prev, c+"."):
				return i, fmt.Sprintf("@dataset column %q overlaps column %q: a field and a path inside it cannot both be columns (draft §3.4.4)", c, prev)
			}
		}
	}
	return -1, ""
}

// checkColumnPaths checks every dotted column against the row type desc:
// each segment names a field, and each segment but the last names a
// singular message field whose type is not written as a literal of its
// own (a Timestamp, Duration, wrapper, pxf.BigInt, pxf.Decimal or
// pxf.BigFloat). A path has no index, so it cannot go through a repeated
// or map field. A pxf.Secret has a block form as well as its literal
// (`pw { value = "x" hint = "h" }`), so a path may go into one. A column without a dot is left to the
// decoder, which reports an unknown field as it does in a body.
func checkColumnPaths(desc protoreflect.MessageDescriptor, cols []string) error {
	if i, msg := columnPathError(cols); i >= 0 {
		return fmt.Errorf("pxf: %s", msg)
	}
	for _, c := range cols {
		if !containsDot(c) {
			continue
		}
		md := desc
		segs := strings.Split(c, ".")
		for k, seg := range segs {
			fd := md.Fields().ByName(protoreflect.Name(seg))
			if fd == nil {
				return fmt.Errorf("pxf: @dataset column %q: %s has no field %q", c, md.FullName(), seg)
			}
			if k == len(segs)-1 {
				break
			}
			at := strings.Join(segs[:k+1], ".")
			switch {
			case fd.IsMap():
				return fmt.Errorf("pxf: @dataset column %q: %q is a map field, and a column path cannot go through one; use a value cell for %q (draft §3.4.4)", c, at, at)
			case fd.IsList():
				return fmt.Errorf("pxf: @dataset column %q: %q is a repeated field, and a column path cannot go through one; use a value cell for %q (draft §3.4.4)", c, at, at)
			case fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind:
				return fmt.Errorf("pxf: @dataset column %q: %q is not a message field", c, at)
			case isLiteralMessageType(fd.Message()) && !isSecret(fd.Message()):
				return fmt.Errorf("pxf: @dataset column %q: %q is a %s, which is written as a literal; a column path cannot go into it (draft §3.4.4)", c, at, fd.Message().FullName())
			}
			md = fd.Message()
		}
	}
	return nil
}
