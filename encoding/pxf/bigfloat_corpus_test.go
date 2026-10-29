// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// The pxf.BigFloat text-form corpus (draft-trendvidia-protowire-01 § PXF
// Annotations; trendvidia/protowire#406, #393 and #410). testdata/bigfloat/
// is vendored verbatim from the spec repository (trendvidia/protowire
// testdata/bigfloat/, PR trendvidia/protowire#408 as amended by #412),
// where generate.py computes every expected value with exact rational
// arithmetic; keep the two in sync when the spec repo changes it. An enc-
// case is PB bytes and the text Marshal must write for them, a dec- case a
// document and the PB bytes decoding it must produce, an err- case a
// document to reject.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func TestBigFloatCorpus(t *testing.T) {
	src, err := os.ReadFile("testdata/bigfloat/bigfloat.proto")
	require.NoError(t, err)
	md := compilePlacementSrc(t, string(src), "bf.v1", "H")
	files, err := filepath.Glob("testdata/bigfloat/*.pxf")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, p := range files {
		name := strings.TrimSuffix(filepath.Base(p), ".pxf")
		t.Run(name, func(t *testing.T) {
			text, err := os.ReadFile(p)
			require.NoError(t, err)
			switch {
			case strings.HasPrefix(name, "enc-"):
				wire, err := os.ReadFile(strings.TrimSuffix(p, ".pxf") + ".binpb")
				require.NoError(t, err)
				msg := dynamicpb.NewMessage(md)
				require.NoError(t, proto.Unmarshal(wire, msg))
				out, err := pxf.Marshal(msg)
				require.NoError(t, err)
				require.Equal(t, string(text), string(out))
			case strings.HasPrefix(name, "dec-"):
				wire, err := os.ReadFile(strings.TrimSuffix(p, ".pxf") + ".binpb")
				require.NoError(t, err)
				msg, err := pxf.UnmarshalDescriptor(text, md)
				require.NoError(t, err)
				got, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
				require.NoError(t, err)
				require.True(t, bytes.Equal(wire, got), "PB bytes\n got %x\nwant %x", got, wire)
			case strings.HasPrefix(name, "err-"):
				// Each reject names its cause, so a case cannot pass on an
				// unrelated error: the field for a misplaced suffix, the
				// precision for the others.
				_, err := pxf.UnmarshalDescriptor(text, md)
				want := "precision"
				if name == "err-suffix-on-int32" {
					want = `field "count"`
				}
				require.ErrorContains(t, err, want)
			default:
				t.Fatalf("%s is neither enc-, dec- nor err-", name)
			}
		})
	}
}
