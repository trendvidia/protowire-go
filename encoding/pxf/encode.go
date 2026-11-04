// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// MarshalOptions configures PXF encoding.
type MarshalOptions struct {
	Indent       string       // indentation per level, default "  "
	EmitDefaults bool         // emit fields with zero values
	TypeURL      string       // emit @type directive if non-empty
	TypeResolver TypeResolver // resolve Any type URLs for sugar encoding; an Any whose body would not read back unchanged stays type_url and value
	NullFields   *Result      // emit null fields from a Result (for messages without null_mask)

	// MaxNestingDepth is the deepest document Marshal writes, counted as a
	// decoder counts it: the root is depth 0, and every '{' or '[' is one
	// descent. A message that needs more is an error, because a reader at
	// the same limit would refuse the document. An Any past the limit is
	// not: it stays as type_url and value. Zero means the package constant.
	// Raise it only with UnmarshalOptions.MaxNestingDepth on every reader.
	MaxNestingDepth int
}

// Marshal formats msg as PXF text with default options.
//
// An unset field is left out, except where a reader that applies
// (pxf.default) and checks (pxf.required) would not read the absent field
// back as unset (#259). A proto3 scalar without presence is written as
// its zero value, and a required list or map as `[]` or `{}`. An unset
// message-typed field with a default is written `null`. An unset
// `optional` or oneof scalar with a default is left out, and reads back
// as its default; `null` would keep it unset, and whether Marshal should
// write that is open (#267). An unset required field with presence is an
// error: a required field rejects `null` (#268), so no text leaves it
// unset.
func Marshal(msg proto.Message) ([]byte, error) {
	return MarshalOptions{}.Marshal(msg)
}

// Marshal formats msg as PXF text.
func (o MarshalOptions) Marshal(msg proto.Message) ([]byte, error) {
	if o.Indent == "" {
		o.Indent = "  "
	}
	var buf bytes.Buffer
	enc := &encoder{buf: &buf, indent: o.Indent, emitDefaults: o.EmitDefaults, resolver: o.TypeResolver, nullFields: o.NullFields, maxDepth: MaxNestingDepth}
	if o.MaxNestingDepth > 0 {
		enc.maxDepth = o.MaxNestingDepth
	}

	// Discover null_mask once at the top level.
	desc := msg.ProtoReflect().Descriptor()
	enc.nullMaskFd = findNullMaskField(desc)
	if enc.nullMaskFd != nil && msg.ProtoReflect().Has(enc.nullMaskFd) {
		enc.nullSet = readNullMask(msg.ProtoReflect(), enc.nullMaskFd)
	} else if o.NullFields != nil {
		enc.nullSet = make(map[string]bool, len(o.NullFields.nullFields))
		for path := range o.NullFields.nullFields {
			enc.nullSet[path] = true
		}
	}

	if o.TypeURL != "" {
		buf.WriteString("@type ")
		buf.WriteString(o.TypeURL)
		buf.WriteString("\n\n")
	}
	if err := enc.encodeMessage(msg.ProtoReflect(), 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type encoder struct {
	buf          *bytes.Buffer
	indent       string
	emitDefaults bool
	maxDepth     int // the deepest level a document may reach
	resolver     TypeResolver
	nullFields   *Result
	nullMaskFd   protoreflect.FieldDescriptor // cached, top-level only
	nullSet      map[string]bool              // cached, top-level only
	pathPrefix   string                       // dotted path prefix for nested null lookup
	skipKeyFd    protoreflect.FieldDescriptor // key field to omit from the NEXT encodeMessage call (keyed-block entry body, draft -01 §3.13)
	scratch      [64]byte                     // scratch buffer for number formatting
}

// nestingError is the error for a message deeper than the writer's limit.
type nestingError struct{ limit int }

func (err nestingError) Error() string {
	return fmt.Sprintf("pxf: nesting depth exceeds MaxNestingDepth=%d", err.limit)
}

// requiredUnsetError is the error for an unset (pxf.required) field that
// has presence: leaving it out is a document a reader rejects (#259), and
// so is writing it `null` (#268), so no text says it is unset.
type requiredUnsetError struct{ field protoreflect.FullName }

func (err requiredUnsetError) Error() string {
	return fmt.Sprintf("pxf: %s is unset, and the field is (pxf.required)", err.field)
}

// unsetField decides how Marshal writes fd, which msg does not set and
// whose annotations are o. A reader applies (pxf.default) to an absent
// field and checks (pxf.required) (#161), so leaving out an annotated
// field would not read back as the message (#259). So a field without
// presence is written as its zero value (write), which the default does
// not replace and which counts as supplied; a required list or map has no
// presence, so it is a write too, of [emptyCollection]. A message-typed
// field is written null, which keeps the default off and leaves it unset.
// An `optional` or oneof scalar is not written, and reads back as its
// default. The draft accepts null on it and leaves it unset
// (trendvidia/protowire#339 option 3), so null could keep it unset too;
// whether to write that is open (#267). A oneof member is not written
// when a sibling is set, since the sibling keeps the default off. An
// unset required field with presence cannot be written: a reader rejects
// it absent and rejects it null (#268). An unannotated field is not
// written.
func unsetField(msg protoreflect.Message, fd protoreflect.FieldDescriptor, o cellOptions) (write, null bool, err error) {
	switch {
	case !o.hasDefault && !o.required:
		return false, false, nil
	case !fd.HasPresence():
		return true, false, nil
	case o.required:
		return false, false, requiredUnsetError{fd.FullName()}
	}
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() && msg.WhichOneof(od) != nil {
		return false, false, nil
	}
	isMsg := fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind
	return false, isMsg, nil
}

// checkDepth refuses a block or list whose contents sit at level. The
// level of a body is the decoder's depth there (HARDENING.md § Recursion).
func (e *encoder) checkDepth(level int) error {
	if level > e.maxDepth {
		return nestingError{e.maxDepth}
	}
	return nil
}

func (e *encoder) writeIndent(level int) {
	for range level {
		e.buf.WriteString(e.indent)
	}
}

// writeFieldPrefix writes: <indent>name =
func (e *encoder) writeFieldPrefix(level int, name protoreflect.Name) {
	e.writeIndent(level)
	e.buf.WriteString(string(name))
	e.buf.WriteString(" = ")
}

// enter extends the null-lookup path by `name.` the way the decoder
// extends its presence path (directDecoder.enter), so a null
// UnmarshalFull recorded inside a body is found where it was recorded.
// It returns the path to restore. A path is built only when there is a
// null set to look it up in.
func (e *encoder) enter(name protoreflect.Name) string {
	return e.extend(name, "")
}

// enterIndex is enter for the i'th element of a list: `name[i].`.
func (e *encoder) enterIndex(name protoreflect.Name, i int) string {
	if e.nullSet == nil {
		return e.pathPrefix
	}
	return e.extend(name, indexSuffix(i))
}

// enterKey is enter for the value of map key k: `name["k"].`.
func (e *encoder) enterKey(name protoreflect.Name, k protoreflect.MapKey) string {
	if e.nullSet == nil {
		return e.pathPrefix
	}
	return e.extend(name, keySuffix(k))
}

func (e *encoder) extend(name protoreflect.Name, sfx string) string {
	saved := e.pathPrefix
	if e.nullSet != nil {
		e.pathPrefix = saved + string(name) + sfx + "."
	}
	return saved
}

func (e *encoder) encodeMessage(msg protoreflect.Message, level int) error {
	fields := msg.Descriptor().Fields()

	// A pending skipKeyFd applies to exactly this body: the entry name
	// of a keyed-block entry already carries the key field's value, so
	// the field is not additionally emitted inside the block (draft -01
	// §3.13). Consume it so nested messages don't inherit the skip.
	skipKey := e.skipKeyFd
	e.skipKeyFd = nil

	if err := e.checkDepth(level); err != nil {
		return err
	}

	var opts []cellOptions // the fields' annotations, read at the first unset field
	for i := range fields.Len() {
		fd := fields.Get(i)
		if skipKey != nil && fd == skipKey {
			continue
		}

		// Skip the root's _null FieldMask field itself. Level 0 is the
		// root: a list element, map value or Any body has an empty path
		// too when no null set is tracked, and its field of the same
		// number is data.
		if e.nullMaskFd != nil && level == 0 && fd.Number() == e.nullMaskFd.Number() {
			continue
		}

		path := e.pathPrefix + string(fd.Name())

		// Emit null for fields in the null set that can hold one. A
		// scalar, repeated or map field named in the set is written as
		// if the set did not name it: null on it is a decode error
		// (nullable; #151).
		if e.nullSet != nil && e.nullSet[path] && nullable(fd) {
			e.writeFieldPrefix(level, fd.Name())
			e.buf.WriteString("null\n")
			continue
		}

		if !msg.Has(fd) {
			if opts == nil {
				opts = datasetCellOptions(msg.Descriptor())
			}
			write, null, err := unsetField(msg, fd, opts[i])
			switch {
			case err != nil && !e.emitDefaults:
				return err
			case null:
				e.writeFieldPrefix(level, fd.Name())
				e.buf.WriteString("null\n")
				continue
			case write && (fd.IsList() || fd.IsMap()):
				if err := e.checkDepth(level + 1); err != nil {
					return err
				}
				e.writeFieldPrefix(level, fd.Name())
				e.buf.WriteString(emptyCollection(fd))
				e.buf.WriteByte('\n')
				continue
			case !write && !e.emitDefaults:
				continue
			}
			// The zero value, written out; falls through below. With
			// EmitDefaults, so is every other unset field, as before: an
			// unset required field with presence then reads back set to
			// zero, which a reader accepts.
		}
		val := msg.Get(fd)

		if fd.IsMap() {
			if err := e.encodeMapField(fd, val, level); err != nil {
				return err
			}
			continue
		}

		if fd.IsList() {
			if err := e.encodeListField(fd, val, level); err != nil {
				return err
			}
			continue
		}

		if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
			if !msg.Has(fd) {
				continue
			}
			if err := e.encodeMessageField(fd, val.Message(), level); err != nil {
				return err
			}
			continue
		}

		e.writeFieldPrefix(level, fd.Name())
		if err := e.writeScalar(fd, val); err != nil {
			return err
		}
		e.buf.WriteByte('\n')
	}
	return nil
}

// readNullMask reads the null_mask FieldMask field and returns a set of null field names.
func readNullMask(msg protoreflect.Message, nmFd protoreflect.FieldDescriptor) map[string]bool {
	fmMsg := msg.Get(nmFd).Message()
	pathsFd := fmMsg.Descriptor().Fields().ByName("paths")
	list := fmMsg.Get(pathsFd).List()
	m := make(map[string]bool, list.Len())
	for i := range list.Len() {
		m[list.Get(i).String()] = true
	}
	return m
}

func (e *encoder) encodeMessageField(fd protoreflect.FieldDescriptor, sub protoreflect.Message, level int) error {
	mdesc := fd.Message()

	if isLiteralMessage(sub) {
		e.writeFieldPrefix(level, fd.Name())
		if err := e.writeLiteralMessage(sub); err != nil {
			return err
		}
		e.buf.WriteByte('\n')
		return nil
	}
	// Any sugar: @type + inline fields
	if isAny(mdesc) && e.resolver != nil {
		encoded, err := e.tryEncodeAny(fd, sub, level)
		if err != nil {
			return err
		}
		if encoded {
			return nil
		}
	}

	e.writeIndent(level)
	e.buf.WriteString(string(fd.Name()))
	e.buf.WriteString(" {\n")
	saved := e.enter(fd.Name())
	err := e.encodeMessage(sub, level+1)
	e.pathPrefix = saved
	if err != nil {
		return err
	}
	e.writeIndent(level)
	e.buf.WriteString("}\n")
	return nil
}

// isLiteralMessageType reports whether a message of type mdesc can be
// written as one literal instead of a block: a Timestamp, a Duration, a
// wrapper, a big number, or a Secret (which needs a block when it
// carries metadata; see isLiteralMessage).
func isLiteralMessageType(mdesc protoreflect.MessageDescriptor) bool {
	return isTimestamp(mdesc) || isDuration(mdesc) || isWrapperType(mdesc) ||
		isBigInt(mdesc) || isDecimal(mdesc) || isBigFloat(mdesc) || isSecret(mdesc)
}

// isLiteralMessage reports whether sub is written as one literal. A
// Secret is, only without a hint or fingerprint: the literal would
// drop that authoring metadata. A Duration is, only within the literal's
// range: see durationFitsLiteral.
func isLiteralMessage(sub protoreflect.Message) bool {
	mdesc := sub.Descriptor()
	if isSecret(mdesc) {
		return !secretHasMetadata(sub)
	}
	if isDuration(mdesc) {
		return durationFitsLiteral(sub)
	}
	return isLiteralMessageType(mdesc)
}

// writeLiteralMessage writes sub, for which isLiteralMessage holds, as
// its literal.
func (e *encoder) writeLiteralMessage(sub protoreflect.Message) error {
	mdesc := sub.Descriptor()
	var s string
	var err error
	switch {
	case isTimestamp(mdesc):
		s = readTimestamp(sub).Format(time.RFC3339Nano)
	case isDuration(mdesc):
		s = readDuration(sub).String()
	case isWrapperType(mdesc), isSecret(mdesc):
		innerFd := mdesc.Fields().ByName("value")
		return e.writeScalar(innerFd, sub.Get(innerFd))
	case isBigInt(mdesc):
		s, err = formatBigInt(sub)
	case isDecimal(mdesc):
		s, err = readDecimalStr(sub)
	case isBigFloat(mdesc):
		s, err = formatBigFloat(sub)
	default:
		return fmt.Errorf("pxf: %s has no literal form", mdesc.FullName())
	}
	if err != nil {
		return err
	}
	e.buf.WriteString(s)
	return nil
}

func (e *encoder) encodeListField(fd protoreflect.FieldDescriptor, val protoreflect.Value, level int) error {
	list := val.List()
	if list.Len() == 0 && !e.emitDefaults {
		return nil
	}

	// Keyed repeated field (draft -01 §3.13): emit the keyed block form
	// whenever every element's key is present, non-empty, and distinct;
	// otherwise fall through to the anonymous list form (elements with
	// absent or duplicate keys can only be represented anonymously).
	if keyFd := KeyField(fd); keyFd != nil && list.Len() > 0 {
		if keyedFormEligible(list, keyFd) {
			return e.encodeKeyedList(fd, list, keyFd, level)
		}
	}

	if err := e.checkDepth(level + 1); err != nil {
		return err
	}
	e.writeFieldPrefix(level, fd.Name())
	e.buf.WriteString("[\n")

	for i := range list.Len() {
		elem := list.Get(i)

		if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
			sub := elem.Message()
			if isLiteralMessage(sub) {
				e.writeIndent(level + 1)
				if err := e.writeLiteralMessage(sub); err != nil {
					return err
				}
			} else {
				e.writeIndent(level + 1)
				e.buf.WriteString("{\n")
				saved := e.enterIndex(fd.Name(), i)
				err := e.encodeMessage(sub, level+2)
				e.pathPrefix = saved
				if err != nil {
					return err
				}
				e.writeIndent(level + 1)
				e.buf.WriteByte('}')
			}
		} else {
			e.writeIndent(level + 1)
			if err := e.writeScalar(fd, elem); err != nil {
				return err
			}
		}

		if i < list.Len()-1 {
			e.buf.WriteByte(',')
		}
		e.buf.WriteByte('\n')
	}

	e.writeIndent(level)
	e.buf.WriteString("]\n")
	return nil
}

// keyedFormEligible reports whether every element of list has a
// non-empty key value that is distinct within the collection — the
// draft -01 §3.13 condition for emitting the keyed block form.
func keyedFormEligible(list protoreflect.List, keyFd protoreflect.FieldDescriptor) bool {
	seen := make(map[string]struct{}, list.Len())
	for i := range list.Len() {
		key := list.Get(i).Message().Get(keyFd).String()
		if key == "" {
			return false
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

// encodeKeyedList emits a keyed repeated field in the keyed block form:
// one named block per element, in list order. Entry names are written
// unquoted when identifier-safe and quoted otherwise; the key field is
// not additionally emitted inside the entry's block.
func (e *encoder) encodeKeyedList(fd protoreflect.FieldDescriptor, list protoreflect.List, keyFd protoreflect.FieldDescriptor, level int) error {
	e.writeIndent(level)
	e.buf.WriteString(string(fd.Name()))
	e.buf.WriteString(" {\n")
	for i := range list.Len() {
		sub := list.Get(i).Message()
		key := sub.Get(keyFd).String()
		e.writeIndent(level + 1)
		if identSafeEntryName(key) {
			e.buf.WriteString(key)
		} else {
			e.writeQuotedString(key)
		}
		e.buf.WriteString(" {\n")
		e.skipKeyFd = keyFd
		saved := e.enterIndex(fd.Name(), i)
		err := e.encodeMessage(sub, level+2)
		e.pathPrefix = saved
		if err != nil {
			return err
		}
		e.writeIndent(level + 1)
		e.buf.WriteString("}\n")
	}
	e.writeIndent(level)
	e.buf.WriteString("}\n")
	return nil
}

func (e *encoder) encodeMapField(fd protoreflect.FieldDescriptor, val protoreflect.Value, level int) error {
	m := val.Map()
	if m.Len() == 0 && !e.emitDefaults {
		return nil
	}

	if err := e.checkDepth(level + 1); err != nil {
		return err
	}
	e.writeFieldPrefix(level, fd.Name())
	e.buf.WriteString("{\n")

	// Collect and sort keys. Pre-format keys once to avoid repeated
	// allocations during sort comparisons.
	type mapKV struct {
		keyStr string
		key    protoreflect.MapKey
		val    protoreflect.Value
	}
	entries := make([]mapKV, 0, m.Len())
	keyFd := fd.MapKey()
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		entries = append(entries, mapKV{formatMapKey(keyFd, k), k, v})
		return true
	})
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].keyStr < entries[j].keyStr
	})

	valFd := fd.MapValue()

	for _, kv := range entries {
		if valFd.Kind() == protoreflect.MessageKind || valFd.Kind() == protoreflect.GroupKind {
			sub := kv.val.Message()
			mdesc := valFd.Message()

			// WKT scalar emission in map-value position. Mirrors the
			// equivalent block in encodeMessageField / encodeListField
			// so a Timestamp / Duration / wrapper / BigInt / Decimal /
			// BigFloat / Secret value renders as `key: <scalar>`
			// rather than `key: { value = <scalar> }`. Secret falls
			// back to block form when hint or fingerprint is set, so
			// authoring metadata round-trips, and a Duration past the
			// literal's range does too, so its value does.
			if isTimestamp(mdesc) {
				t := readTimestamp(sub)
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				e.buf.WriteString(t.Format(time.RFC3339Nano))
				e.buf.WriteByte('\n')
				continue
			}
			if isDuration(mdesc) && isLiteralMessage(sub) {
				d := readDuration(sub)
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				e.buf.WriteString(d.String())
				e.buf.WriteByte('\n')
				continue
			}
			if isWrapperType(mdesc) {
				innerFd := mdesc.Fields().ByName("value")
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				if err := e.writeScalar(innerFd, sub.Get(innerFd)); err != nil {
					return err
				}
				e.buf.WriteByte('\n')
				continue
			}
			if isBigInt(mdesc) {
				s, err := formatBigInt(sub)
				if err != nil {
					return err
				}
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				e.buf.WriteString(s)
				e.buf.WriteByte('\n')
				continue
			}
			if isDecimal(mdesc) {
				s, err := readDecimalStr(sub)
				if err != nil {
					return err
				}
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				e.buf.WriteString(s)
				e.buf.WriteByte('\n')
				continue
			}
			if isBigFloat(mdesc) {
				s, err := formatBigFloat(sub)
				if err != nil {
					return err
				}
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				e.buf.WriteString(s)
				e.buf.WriteByte('\n')
				continue
			}
			if isSecret(mdesc) && !secretHasMetadata(sub) {
				innerFd := mdesc.Fields().ByName("value")
				e.writeIndent(level + 1)
				e.buf.WriteString(kv.keyStr)
				e.buf.WriteString(": ")
				if err := e.writeScalar(innerFd, sub.Get(innerFd)); err != nil {
					return err
				}
				e.buf.WriteByte('\n')
				continue
			}

			e.writeIndent(level + 1)
			e.buf.WriteString(kv.keyStr)
			e.buf.WriteString(": {\n")
			saved := e.enterKey(fd.Name(), kv.key)
			err := e.encodeMessage(sub, level+2)
			e.pathPrefix = saved
			if err != nil {
				return err
			}
			e.writeIndent(level + 1)
			e.buf.WriteString("}\n")
		} else {
			e.writeIndent(level + 1)
			e.buf.WriteString(kv.keyStr)
			e.buf.WriteString(": ")
			if err := e.writeScalar(valFd, kv.val); err != nil {
				return err
			}
			e.buf.WriteByte('\n')
		}
	}

	e.writeIndent(level)
	e.buf.WriteString("}\n")
	return nil
}

// writeScalar writes a scalar value directly to the buffer (no intermediate string).
func (e *encoder) writeScalar(fd protoreflect.FieldDescriptor, val protoreflect.Value) error {
	switch fd.Kind() {
	case protoreflect.StringKind:
		e.writeQuotedString(val.String())
	case protoreflect.BoolKind:
		if val.Bool() {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		b := strconv.AppendInt(e.scratch[:0], val.Int(), 10)
		e.buf.Write(b)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		b := strconv.AppendUint(e.scratch[:0], val.Uint(), 10)
		e.buf.Write(b)
	case protoreflect.FloatKind:
		e.writeFloat(val.Float(), 32)
	case protoreflect.DoubleKind:
		e.writeFloat(val.Float(), 64)
	case protoreflect.BytesKind:
		e.buf.WriteString(`b"`)
		e.buf.WriteString(base64.StdEncoding.EncodeToString(val.Bytes()))
		e.buf.WriteByte('"')
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByNumber(val.Enum())
		if ev != nil {
			e.buf.WriteString(string(ev.Name()))
		} else {
			b := strconv.AppendInt(e.scratch[:0], int64(val.Enum()), 10)
			e.buf.Write(b)
		}
	default:
		return fmt.Errorf("unsupported kind: %s", fd.Kind())
	}
	return nil
}

func (e *encoder) writeFloat(f float64, bits int) {
	switch {
	case math.IsInf(f, 1):
		e.buf.WriteString("inf")
	case math.IsInf(f, -1):
		e.buf.WriteString("-inf")
	case math.IsNaN(f):
		e.buf.WriteString("nan")
	default:
		b := strconv.AppendFloat(e.scratch[:0], f, 'g', -1, bits)
		e.buf.Write(b)
	}
}

// writeQuotedString writes a Go-style quoted string directly to the buffer.
func (e *encoder) writeQuotedString(s string) {
	e.buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '"':
			e.buf.WriteString(`\"`)
		case '\\':
			e.buf.WriteString(`\\`)
		case '\n':
			e.buf.WriteString(`\n`)
		case '\r':
			e.buf.WriteString(`\r`)
		case '\t':
			e.buf.WriteString(`\t`)
		default:
			if ch < 0x20 {
				e.buf.WriteString(`\x`)
				e.buf.WriteByte("0123456789abcdef"[ch>>4])
				e.buf.WriteByte("0123456789abcdef"[ch&0xf])
			} else {
				e.buf.WriteByte(ch)
			}
		}
	}
	e.buf.WriteByte('"')
}

func formatMapKey(fd protoreflect.FieldDescriptor, key protoreflect.MapKey) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		s := key.String()
		if isValidIdent(s) {
			return s
		}
		return strconv.Quote(s)
	case protoreflect.BoolKind:
		if key.Bool() {
			return "true"
		}
		return "false"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(key.Int(), 10)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(key.Uint(), 10)
	default:
		return fmt.Sprint(key.Interface())
	}
}

// tryEncodeAny writes a google.protobuf.Any as a type entry and a body.
// It returns false, having written nothing, to leave the Any to the block
// of its own fields, type_url and value: when the type does not resolve,
// when the value does not parse, and when the body would not read back as
// the same value (#232). An Any at the depth limit is not unpacked at all:
// its block has no room for a body's submessages, and stopping here
// bounds a chain of Anys, which proto.Unmarshal does not, since each one
// parses its own bytes with a fresh recursion budget.
func (e *encoder) tryEncodeAny(fd protoreflect.FieldDescriptor, anyMsg protoreflect.Message, level int) (bool, error) {
	anyDesc := fd.Message()
	typeURL := anyMsg.Get(anyDesc.Fields().ByName("type_url")).String()
	valueBytes := anyMsg.Get(anyDesc.Fields().ByName("value")).Bytes()

	if typeURL == "" || level >= e.maxDepth {
		return false, nil
	}

	innerDesc, err := e.resolver.FindMessageByURL(typeURL)
	if err != nil {
		return false, nil // can't resolve, fall back
	}

	inner := dynamicpb.NewMessage(innerDesc)
	if !parseAnyValue(valueBytes, inner) {
		return false, nil
	}

	var body bytes.Buffer
	out := e.buf
	e.buf = &body
	saved := e.enter(fd.Name())
	err = e.encodeMessage(inner.ProtoReflect(), level+1)
	e.pathPrefix = saved
	e.buf = out
	if errors.As(err, new(nestingError)) {
		return false, nil // the value is too deep to unpack here; its bytes are not
	}
	if errors.As(err, new(requiredUnsetError)) {
		return false, nil // a body would be rejected; the value's bytes are not
	}
	if err != nil {
		return false, err
	}
	if !anyBodyReadsBack(body.Bytes(), innerDesc, e.resolver, level, e.maxDepth, valueBytes) {
		return false, nil
	}

	e.writeIndent(level)
	e.buf.WriteString(string(fd.Name()))
	e.buf.WriteString(" {\n")

	e.writeIndent(level + 1)
	e.buf.WriteString("@type = ")
	e.writeQuotedString(typeURL)
	e.buf.WriteByte('\n')

	e.buf.Write(body.Bytes())

	e.writeIndent(level)
	e.buf.WriteString("}\n")
	return true, nil
}

// parseAnyValue parses value, bytes that nothing has checked, into inner
// and reports whether it could. proto.Unmarshal panics rather than failing
// on one such value: a dynamicpb map entry with a good key followed by a
// key of the wrong wire type (protobuf-go v1.36.12, unmarshalMap). That
// value is as unparsable as any other, so it takes the same fallback.
func parseAnyValue(value []byte, inner proto.Message) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return proto.Unmarshal(value, inner) == nil
}

// isValidIdent reports whether a string map key is identifier-safe: it
// matches the identifier production (ident-start followed by ident-part
// bytes, dots included) and is not one of the value keywords null, true
// and false, which bare are no key at all or a bool key. Marshal writes a
// string map key bare exactly when this holds, and FormatDocument
// unquotes a quoted one under the same test, so the two agree on every
// key both can produce (protowire#306). It is the test keyed entry names
// use, identSafeEntryName: one identifier-safe rule for the document,
// as draft -01 § Entries and Keys defines it (protowire#313; the
// alphabet used to stop at [A-Za-z0-9_], so "a.b" was written quoted).
func isValidIdent(s string) bool { return identSafeEntryName(s) }
