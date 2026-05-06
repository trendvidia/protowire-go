// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package envelope provides the standard API response envelope for
// cross-system communication. It defines a uniform structure that
// separates transport errors from application errors and carries
// machine-readable codes with positional format arguments for
// client-side localization.
//
// Wire format: structs use protowire tags for binary protobuf encoding
// and are also directly representable in PXF and JSON.
//
// # Concurrency
//
// The builder constructors ([OK], [Err], [TransportErr], [NewAppError])
// and the read-only query methods ([Envelope.IsOK],
// [Envelope.IsTransportError], [Envelope.IsAppError],
// [Envelope.ErrorCode], [Envelope.FieldErrors]) are safe for concurrent
// use on independent values.
//
// The chainable mutators ([AppError.WithField], [AppError.WithMeta])
// modify the receiver in place and are not safe to call concurrently
// on the same [AppError] without external synchronization. Treat the
// builder pattern as single-goroutine.
//
// [Encoder] and [Decoder] (the length-prefixed stream wrappers) hold a
// reusable scratch buffer and are not safe for concurrent use; create
// one per goroutine, or guard with a mutex.
package envelope

// Envelope wraps an API response with transport metadata, an optional
// success payload, and an optional application error.
type Envelope struct {
	Status         int32     `protowire:"1" json:"status"`
	TransportError string    `protowire:"2" json:"transport_error,omitempty"`
	Data           []byte    `protowire:"3" json:"data,omitempty"`
	Error          *AppError `protowire:"4" json:"error,omitempty"`
}

// AppError represents an application-level error.
type AppError struct {
	Code     string            `protowire:"1" json:"code"`
	Message  string            `protowire:"2" json:"message,omitempty"`
	Args     []string          `protowire:"3" json:"args,omitempty"`
	Details  []*FieldError     `protowire:"4" json:"details,omitempty"`
	Metadata map[string]string `protowire:"5" json:"metadata,omitempty"`
}

// FieldError represents a validation error on a specific field.
type FieldError struct {
	Field   string   `protowire:"1" json:"field"`
	Code    string   `protowire:"2" json:"code"`
	Message string   `protowire:"3" json:"message,omitempty"`
	Args    []string `protowire:"4" json:"args,omitempty"`
}

// --- Builders ---

// OK creates a success envelope with the given status and raw data payload.
func OK(status int32, data []byte) *Envelope {
	return &Envelope{Status: status, Data: data}
}

// Err creates an error envelope.
func Err(status int32, code, message string, args ...string) *Envelope {
	return &Envelope{
		Status: status,
		Error:  &AppError{Code: code, Message: message, Args: args},
	}
}

// TransportErr creates a transport-level error envelope.
func TransportErr(err string) *Envelope {
	return &Envelope{TransportError: err}
}

// NewAppError creates an AppError with code, message, and optional format args.
func NewAppError(code, message string, args ...string) *AppError {
	return &AppError{Code: code, Message: message, Args: args}
}

// WithField adds a field error to an AppError and returns it for chaining.
func (e *AppError) WithField(field, code, message string, args ...string) *AppError {
	e.Details = append(e.Details, &FieldError{
		Field:   field,
		Code:    code,
		Message: message,
		Args:    args,
	})
	return e
}

// WithMeta adds a metadata key-value pair and returns the error for chaining.
func (e *AppError) WithMeta(key, value string) *AppError {
	if e.Metadata == nil {
		e.Metadata = make(map[string]string)
	}
	e.Metadata[key] = value
	return e
}

// --- Queries ---

// IsOK reports whether the envelope represents a successful response.
func (e *Envelope) IsOK() bool {
	return e.TransportError == "" && e.Error == nil
}

// IsTransportError reports whether the envelope has a transport-level error.
func (e *Envelope) IsTransportError() bool {
	return e.TransportError != ""
}

// IsAppError reports whether the envelope has an application-level error.
func (e *Envelope) IsAppError() bool {
	return e.Error != nil
}

// ErrorCode returns the application error code, or empty string if no error.
func (e *Envelope) ErrorCode() string {
	if e.Error != nil {
		return e.Error.Code
	}
	return ""
}

// FieldErrors returns field errors indexed by field name for easy lookup.
func (e *Envelope) FieldErrors() map[string]*FieldError {
	if e.Error == nil || len(e.Error.Details) == 0 {
		return nil
	}
	m := make(map[string]*FieldError, len(e.Error.Details))
	for _, fe := range e.Error.Details {
		m[fe.Field] = fe
	}
	return m
}
