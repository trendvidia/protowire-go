// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package envelope_test

import (
	"fmt"

	"github.com/trendvidia/protowire-go/envelope"
)

// ExampleOK shows the canonical success-envelope shape.
func ExampleOK() {
	env := envelope.OK(200, []byte("hello"))
	fmt.Println(env.IsOK(), env.Status, string(env.Data))
	// Output: true 200 hello
}

// ExampleErr shows an application-level error with a machine-readable
// code and positional format arguments.
func ExampleErr() {
	env := envelope.Err(404, "user.not_found", "user %s does not exist", "alice")
	fmt.Println(env.IsAppError(), env.ErrorCode(), env.Error.Message, env.Error.Args)
	// Output: true user.not_found user %s does not exist [alice]
}

// ExampleAppError_WithField demonstrates attaching field-level
// validation errors to an application error.
func ExampleAppError_WithField() {
	err := envelope.NewAppError("validation.failed", "request was invalid").
		WithField("email", "format.invalid", "must be a valid email").
		WithField("age", "value.out_of_range", "must be between %d and %d", "0", "120")

	for _, fe := range err.Details {
		fmt.Println(fe.Field, fe.Code)
	}
	// Output:
	// email format.invalid
	// age value.out_of_range
}

// ExampleEnvelope_FieldErrors shows looking up field errors by name
// from the receiving side.
func ExampleEnvelope_FieldErrors() {
	env := &envelope.Envelope{
		Status: 400,
		Error: envelope.NewAppError("validation.failed", "invalid").
			WithField("email", "format.invalid", "bad format"),
	}

	if fe := env.FieldErrors()["email"]; fe != nil {
		fmt.Println(fe.Field, fe.Code, fe.Message)
	}
	// Output: email format.invalid bad format
}
