// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package pxf implements the PXF text format codec for protobuf messages.
//
// PXF (Protocol-buffer eXchange Format) is a human-editable, comment-aware
// text encoding for protobuf messages. The format spec, grammar, and
// canonical fixtures live in the sibling repository
// trendvidia/protowire; this package is the Go reference implementation.
//
// # Choosing a code path
//
// Three top-level entry points serve different needs:
//
//   - [Unmarshal] / [Marshal] — fast path. A fused single-pass
//     lexer+decoder writes directly into the [google.golang.org/protobuf/proto.Message]
//     with zero-copy token strings and no AST allocations. Use this in
//     hot paths or when you do not need the source comments.
//   - [UnmarshalFull] — like [Unmarshal] but returns a [Result] tracking
//     which fields were set, null, or absent, validating required
//     fields, and applying defaults from (pxf.required) /
//     (pxf.default) annotations.
//   - [Parse] / [FormatDocument] — AST path. Produces a [Document] with
//     comments attached to entries. Use when you need to round-trip a
//     PXF document while preserving its formatting and comments.
//
// # Concurrency
//
// The package-level functions ([Unmarshal], [Marshal], [Parse],
// [FormatDocument]) are safe for concurrent use — they do not share
// mutable state. The [MarshalOptions] and [UnmarshalOptions] value
// types are also safe to share, provided the [TypeResolver] embedded
// in them is concurrency-safe (the registry-client resolver from
// trendvidia/protoregistry is).
//
// A [Result] returned from [UnmarshalFull] is not safe for concurrent
// modification, but read-only access from multiple goroutines is fine.
package pxf
