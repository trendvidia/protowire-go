// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Dumps a canonical envelope-stream (varint-delimited) as hex, for
// cross-port wire-compat checking. Three frames covering OK, structured
// Err with field detail + meta, and TransportErr.
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/trendvidia/protowire-go/envelope"
)

func main() {
	ok := envelope.OK(200, []byte{0xDE, 0xAD, 0xBE, 0xEF})

	bad := envelope.Err(402, "INSUFFICIENT_FUNDS", "balance too low",
		"$3.50", "$10.00")
	bad.Error.
		WithField("amount", "MIN_VALUE", "below minimum", "10.00").
		WithMeta("request_id", "req-123")

	transport := envelope.TransportErr("connection refused")

	var buf bytes.Buffer
	enc := envelope.NewEncoder(&buf)
	for i, e := range []*envelope.Envelope{ok, bad, transport} {
		if err := enc.Encode(e); err != nil {
			fmt.Fprintf(os.Stderr, "frame %d: %v\n", i, err)
			os.Exit(1)
		}
	}
	fmt.Println(hex.EncodeToString(buf.Bytes()))
}
