// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

//go:build race

package pxf

// raceEnabled is true under -race, where tests that do input-sized work
// for a count (not a time) run on smaller inputs.
const raceEnabled = true
