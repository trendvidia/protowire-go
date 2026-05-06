// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package envelope

import (
	"reflect"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

func TestBinaryRoundTrip_OK(t *testing.T) {
	orig := OK(200, []byte{0xDE, 0xAD, 0xBE, 0xEF})

	data, err := pb.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Envelope{}
	if err := pb.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("OK round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
	if !got.IsOK() {
		t.Error("expected IsOK after round-trip")
	}
}

func TestBinaryRoundTrip_TransportErr(t *testing.T) {
	orig := TransportErr("connection refused")

	data, err := pb.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Envelope{}
	if err := pb.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("TransportErr round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
	if !got.IsTransportError() {
		t.Error("expected IsTransportError after round-trip")
	}
}

func TestBinaryRoundTrip_AppError_WithFieldsAndMetadata(t *testing.T) {
	// This test exercises the binary path through the metadata map field —
	// the case that previously failed with "unsupported kind map" until the
	// reflect.Map case was added to encoding/pb.
	ae := NewAppError("INSUFFICIENT_FUNDS", "balance too low", "$3.50", "$10.00").
		WithField("amount", "MIN_VALUE", "below minimum", "10.00").
		WithField("currency", "INVALID", "unsupported currency").
		WithMeta("request_id", "req-123").
		WithMeta("retry_after", "30")
	orig := &Envelope{Status: 402, Error: ae}

	data, err := pb.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Envelope{}
	if err := pb.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("AppError round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
	if !got.IsAppError() {
		t.Error("expected IsAppError after round-trip")
	}
	if got.ErrorCode() != "INSUFFICIENT_FUNDS" {
		t.Errorf("ErrorCode: got %q, want INSUFFICIENT_FUNDS", got.ErrorCode())
	}
	if len(got.Error.Details) != 2 {
		t.Errorf("Details: got %d entries, want 2", len(got.Error.Details))
	}
	if got.Error.Metadata["request_id"] != "req-123" {
		t.Errorf("Metadata[request_id]: got %q, want req-123", got.Error.Metadata["request_id"])
	}
}

func TestBinaryRoundTrip_ZeroEnvelope(t *testing.T) {
	orig := &Envelope{}

	data, err := pb.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty bytes for zero envelope, got %d bytes", len(data))
	}

	got := &Envelope{}
	if err := pb.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.IsOK() {
		t.Error("expected IsOK for zero envelope")
	}
}

func TestBinaryRoundTrip_AppErrorBuilders(t *testing.T) {
	// Err() builder followed by binary round-trip — the common production path.
	orig := Err(400, "VALIDATION", "fields invalid", "context")

	data, err := pb.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Envelope{}
	if err := pb.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("Err builder round-trip mismatch:\n  orig: %+v\n  got:  %+v", orig, got)
	}
}
