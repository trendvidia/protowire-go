// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Dumps a canonical pb-stream (varint-delimited) as hex, for cross-port
// wire-compat checking. Three frames; the schema-equivalent definition
// is documented alongside the spec fixtures (../protowire/testdata/stream/).
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

type StreamMsg struct {
	N    int    `protowire:"1"`
	Text string `protowire:"2"`
	Data []byte `protowire:"3"`
}

func main() {
	msgs := []*StreamMsg{
		{N: 1, Text: "hello"},
		{N: 42, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{N: 1 << 20, Text: "third"},
	}

	var buf bytes.Buffer
	enc := pb.NewEncoder(&buf)
	for i, m := range msgs {
		if err := enc.Encode(m); err != nil {
			fmt.Fprintf(os.Stderr, "frame %d: %v\n", i, err)
			os.Exit(1)
		}
	}
	fmt.Println(hex.EncodeToString(buf.Bytes()))
}
