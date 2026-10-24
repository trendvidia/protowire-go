// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"reflect"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// pdField is what postDecode needs to know of one field before it looks
// at the document: its (pxf.default) and (pxf.required), and whether it
// holds message bodies (a message field, or a list or map of messages)
// in which some field carries one.
type pdField struct {
	opts             pxfOptions
	annotated, visit bool
}

// postDecodePlans memoizes postDecodePlan per message descriptor,
// bounded like the other descriptor caches (maxCachedDescriptors). The
// post-decode pass visits every list element and map value (#161), so
// without the plan it built a path and looked up presence for every
// field of every element, annotated or not.
var (
	postDecodePlans     sync.Map // protoreflect.MessageDescriptor -> []pdField
	postDecodePlanCount atomic.Int64
)

// postDecodePlan returns desc's pdFields, indexed like desc.Fields().
// The result is shared and must not be modified.
func postDecodePlan(desc protoreflect.MessageDescriptor) []pdField {
	// A sync.Map panics on an unhashable key; see datasetCellOptions.
	cacheable := reflect.TypeOf(desc).Comparable()
	if cacheable {
		if v, hit := postDecodePlans.Load(desc); hit {
			return v.([]pdField)
		}
	}
	fields := desc.Fields()
	out := make([]pdField, fields.Len())
	for i := range out {
		fd := fields.Get(i)
		o := pxfFieldOptions(fd)
		out[i].opts = o
		out[i].annotated = o.defSet || o.required
		body := fd
		if fd.IsMap() {
			body = fd.MapValue()
		}
		if body.Kind() == protoreflect.MessageKind || body.Kind() == protoreflect.GroupKind {
			out[i].visit = hasDefaultOrRequired(body.Message(), map[protoreflect.FullName]bool{})
		}
	}
	if cacheable && postDecodePlanCount.Load() < maxCachedDescriptors {
		if _, loaded := postDecodePlans.LoadOrStore(desc, out); !loaded {
			postDecodePlanCount.Add(1)
		}
	}
	return out
}
