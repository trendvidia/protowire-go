// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Dumps a canonical envelope's pb-encoded bytes as hex, for cross-port
// wire-compat checking. The same canonical value is constructed in the
// C++ and TypeScript ports (cmd/dump_envelope, scripts/dump-envelope.ts).
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/trendvidia/protowire-go/encoding/pb"
	"github.com/trendvidia/protowire-go/envelope"
)

func main() {
	env := envelope.Err(402, "INSUFFICIENT_FUNDS", "balance too low",
		"$3.50", "$10.00")
	env.Data = []byte{0xDE, 0xAD, 0xBE, 0xEF}
	env.Error.
		WithField("amount", "MIN_VALUE", "below minimum", "10.00").
		WithMeta("request_id", "req-123")

	data, err := pb.Marshal(env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hex.EncodeToString(data))
}
