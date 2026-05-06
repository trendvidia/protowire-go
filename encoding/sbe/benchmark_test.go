// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
	"github.com/trendvidia/protowire-go/encoding/sbe"
)

const benchProtoSrc = `
syntax = "proto3";
package bench.v1;
import "sbe/annotations.proto";

option (sbe.schema_id) = 1;
option (sbe.version) = 0;

message Order {
  option (sbe.template_id) = 1;

  uint64 order_id = 1;
  uint64 cl_ord_id = 2;
  string account = 3 [(sbe.length) = 16];
  string symbol = 4 [(sbe.length) = 8];
  uint32 side = 5 [(sbe.encoding) = "uint8"];
  int64 price = 6;
  uint32 quantity = 7;
  uint32 order_type = 8 [(sbe.encoding) = "uint8"];
  uint32 time_in_force = 9 [(sbe.encoding) = "uint8"];
  uint64 transact_time = 10;

  message Fill {
    int64 fill_price = 1;
    uint32 fill_qty = 2;
    uint64 fill_id = 3;
    uint64 exec_time = 4;
  }
  repeated Fill fills = 11;
}
`

const benchPXFText = `
order_id = 1001
cl_ord_id = 2001
account = "ACCT-001"
symbol = "AAPL"
side = 1
price = 19150
quantity = 100
order_type = 2
time_in_force = 1
transact_time = 1719500400000000000
fills = [
  {
    fill_price = 19155
    fill_qty = 25
    fill_id = 5001
    exec_time = 1719500400000000100
  }
  {
    fill_price = 19160
    fill_qty = 50
    fill_id = 5002
    exec_time = 1719500400000000200
  }
  {
    fill_price = 19165
    fill_qty = 25
    fill_id = 5003
    exec_time = 1719500400000000300
  }
]
`

var (
	benchMsgDesc protoreflect.MessageDescriptor
	benchCodec   *sbe.Codec
)

func init() {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"sbe/annotations.proto": sbeAnnotationsSrc,
					"bench.proto":           benchProtoSrc,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "bench.proto")
	if err != nil {
		panic(err)
	}
	for _, f := range result {
		if f.Path() == "bench.proto" {
			benchMsgDesc = f.Messages().ByName("Order")
			benchCodec, err = sbe.NewCodec(f)
			if err != nil {
				panic(err)
			}
			return
		}
	}
	panic("bench.proto not found")
}

func makeBenchMessage(b *testing.B) *dynamicpb.Message {
	b.Helper()
	msg, err := pxf.UnmarshalDescriptor([]byte(benchPXFText), benchMsgDesc)
	require.NoError(b, err)
	return msg
}

func BenchmarkSBEMarshal(b *testing.B) {
	msg := makeBenchMessage(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := benchCodec.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSBEUnmarshal(b *testing.B) {
	msg := makeBenchMessage(b)
	data, err := benchCodec.Marshal(msg)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		m := dynamicpb.NewMessage(benchMsgDesc)
		if err := benchCodec.Unmarshal(data, m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPXFMarshal(b *testing.B) {
	msg := makeBenchMessage(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := pxf.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPXFUnmarshal(b *testing.B) {
	data := []byte(benchPXFText)
	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		_, err := pxf.UnmarshalDescriptor(data, benchMsgDesc)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoMarshal(b *testing.B) {
	msg := makeBenchMessage(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := proto.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoUnmarshal(b *testing.B) {
	msg := makeBenchMessage(b)
	data, err := proto.Marshal(msg)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		m := dynamicpb.NewMessage(benchMsgDesc)
		if err := proto.Unmarshal(data, m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSBEViewRead(b *testing.B) {
	msg := makeBenchMessage(b)
	data, err := benchCodec.Marshal(msg)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		v, _ := benchCodec.View(data)
		_ = v.Uint("order_id")
		_ = v.Uint("cl_ord_id")
		_ = v.String("account")
		_ = v.String("symbol")
		_ = v.Uint("side")
		_ = v.Int("price")
		_ = v.Uint("quantity")
		_ = v.Uint("order_type")
		_ = v.Uint("time_in_force")
		_ = v.Uint("transact_time")
		fills := v.Group("fills")
		for i := range fills.Len() {
			e := fills.Entry(i)
			_ = e.Int("fill_price")
			_ = e.Uint("fill_qty")
			_ = e.Uint("fill_id")
			_ = e.Uint("exec_time")
		}
	}
}
