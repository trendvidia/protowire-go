// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"gopkg.in/yaml.v3"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const benchProtoSrc = `
syntax = "proto3";
package bench.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";

message Config {
  string hostname = 1;
  int32 port = 2;
  bool enabled = 3;
  double weight = 4;
  Status status = 5;
  repeated string tags = 6;
  TLS tls = 7;
  map<string, string> labels = 8;
  repeated Endpoint endpoints = 9;
  google.protobuf.Timestamp created_at = 10;
  google.protobuf.Duration timeout = 11;
}

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_SERVING = 1;
}

message TLS {
  string cert_file = 1;
  string key_file = 2;
  bool verify = 3;
}

message Endpoint {
  string path = 1;
  string method = 2;
  int32 timeout_ms = 3;
}
`

const benchPXF = `
hostname = "web-01.prod.example.com"
port = 8443
enabled = true
weight = 0.85
status = STATUS_SERVING
tags = ["production", "us-east", "frontend", "critical"]
tls {
  cert_file = "/etc/ssl/certs/server.pem"
  key_file = "/etc/ssl/private/server.key"
  verify = true
}
labels = {
  env: "production"
  team: "platform"
  region: "us-east-1"
  tier: "frontend"
}
endpoints = [
  {
    path = "/api/v1/users"
    method = "GET"
    timeout_ms = 5000
  }
  {
    path = "/api/v1/orders"
    method = "POST"
    timeout_ms = 10000
  }
  {
    path = "/health"
    method = "GET"
    timeout_ms = 1000
  }
]
created_at = 2024-06-15T12:00:00Z
timeout = 30s
`

var benchDesc protoreflect.MessageDescriptor

func init() {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"bench.proto": benchProtoSrc,
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
			benchDesc = f.Messages().ByName("Config")
			return
		}
	}
	panic("bench.proto not found")
}

func benchMessage(b *testing.B) *dynamicpb.Message {
	b.Helper()
	msg, err := pxf.UnmarshalDescriptor([]byte(benchPXF), benchDesc)
	require.NoError(b, err)
	return msg
}

func BenchmarkPXFUnmarshal(b *testing.B) {
	data := []byte(benchPXF)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := pxf.UnmarshalDescriptor(data, benchDesc)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(len(data)))
}

func BenchmarkPXFMarshal(b *testing.B) {
	msg := benchMessage(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := pxf.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONUnmarshal(b *testing.B) {
	msg := benchMessage(b)
	jsonData, err := protojson.Marshal(msg)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		m := dynamicpb.NewMessage(benchDesc)
		if err := protojson.Unmarshal(jsonData, m); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(len(jsonData)))
}

func BenchmarkJSONMarshal(b *testing.B) {
	msg := benchMessage(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := protojson.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoMarshal(b *testing.B) {
	msg := benchMessage(b)
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
	msg := benchMessage(b)
	data, err := proto.Marshal(msg)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		m := dynamicpb.NewMessage(benchDesc)
		if err := proto.Unmarshal(data, m); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(len(data)))
}

// YAML benchmarks: since there is no proto-native YAML library, we measure
// the realistic path: proto → JSON → generic map → YAML (and reverse).
// This is how YAML is actually used with protobuf in practice.

func yamlDataFromProto(b *testing.B) []byte {
	b.Helper()
	msg := benchMessage(b)
	jsonBytes, err := protojson.Marshal(msg)
	require.NoError(b, err)
	var m map[string]any
	require.NoError(b, json.Unmarshal(jsonBytes, &m))
	yamlBytes, err := yaml.Marshal(m)
	require.NoError(b, err)
	return yamlBytes
}

func BenchmarkYAMLMarshal(b *testing.B) {
	msg := benchMessage(b)
	jsonBytes, err := protojson.Marshal(msg)
	require.NoError(b, err)
	var m map[string]any
	require.NoError(b, json.Unmarshal(jsonBytes, &m))

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := yaml.Marshal(m)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkYAMLUnmarshal(b *testing.B) {
	data := yamlDataFromProto(b)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(len(data)))
}
