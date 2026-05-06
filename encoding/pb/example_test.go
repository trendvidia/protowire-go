// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

import (
	"bytes"
	"fmt"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// Example demonstrates encoding and decoding a Go struct with
// protowire field-number tags. The output is standard protobuf binary
// and is wire-compatible with any .proto message that uses the same
// field numbers and matching types.
func Example() {
	type Endpoint struct {
		Path   string `protowire:"1"`
		Method string `protowire:"2"`
		Port   int    `protowire:"3"`
	}
	type Config struct {
		Hostname  string      `protowire:"1"`
		Enabled   bool        `protowire:"2"`
		Endpoints []*Endpoint `protowire:"3"`
	}

	src := &Config{
		Hostname: "web-01",
		Enabled:  true,
		Endpoints: []*Endpoint{
			{Path: "/api", Method: "GET", Port: 8080},
		},
	}

	data, err := pb.Marshal(src)
	if err != nil {
		fmt.Println("marshal:", err)
		return
	}

	var dst Config
	if err := pb.Unmarshal(data, &dst); err != nil {
		fmt.Println("unmarshal:", err)
		return
	}

	fmt.Println(dst.Hostname, dst.Enabled, dst.Endpoints[0].Path, dst.Endpoints[0].Port)
	// Output: web-01 true /api 8080
}

// ExampleEncoder demonstrates the length-prefixed stream API for
// writing multiple messages back-to-back.
func ExampleEncoder() {
	type Tick struct {
		Symbol string `protowire:"1"`
		Price  int64  `protowire:"2"`
	}

	var buf bytes.Buffer
	enc := pb.NewEncoder(&buf)
	_ = enc.Encode(&Tick{Symbol: "AAPL", Price: 18900})
	_ = enc.Encode(&Tick{Symbol: "GOOG", Price: 14250})

	dec := pb.NewDecoder(&buf)
	for {
		var t Tick
		if err := dec.Decode(&t); err != nil {
			break
		}
		fmt.Println(t.Symbol, t.Price)
	}
	// Output:
	// AAPL 18900
	// GOOG 14250
}
