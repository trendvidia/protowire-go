// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const elemBenchSrc = `
syntax = "proto3";
package elembench.v1;
import "pxf/annotations.proto";
message Doc {
  repeated Ann ann = 1;
  repeated Plain plain = 2;
}
message Ann {
  string id = 1;
  string label = 2 [(pxf.default) = "dflt"];
  int32 n = 3;
  string note = 4;
}
message Plain {
  string id = 1;
  string label = 2;
  int32 n = 3;
  string note = 4;
}
`

// BenchmarkPXFUnmarshalFullElements decodes 1000 list elements through
// UnmarshalFull, whose post-decode pass visits every element (#161):
// "annotated" elements carry a default to apply, "plain" ones none.
func BenchmarkPXFUnmarshalFullElements(b *testing.B) {
	for _, field := range []string{"ann", "plain"} {
		b.Run(field, func(b *testing.B) {
			var sb strings.Builder
			sb.WriteString(field + " = [\n")
			for i := range 1000 {
				fmt.Fprintf(&sb, "  { id = \"e%d\" n = %d note = \"x\" }\n", i, i)
			}
			sb.WriteString("]\n")
			data := []byte(sb.String())
			desc := compilePlacementSrc(b, elemBenchSrc, "elembench.v1", "Doc")
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := pxf.UnmarshalFull(data, dynamicpb.NewMessage(desc)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
