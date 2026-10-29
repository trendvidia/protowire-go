package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// An integer key with '=' or '{' is a grammar error, not an unknown
// field, so DiscardUnknown does not skip it: the decoder reports what
// Parse reports, with or without the option, before or after a known
// field (#196).
func TestDiscardUnknownIntegerKey(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	cases := []struct{ name, src, want string }{
		{"assign first", "1 = 2\n", "field assignment with '=' requires an identifier or string key"},
		{"assign after known field", "string_field = \"x\"\n1 = 2\n", "field assignment with '=' requires an identifier or string key"},
		{"block first", "1 { }\n", "submessage block requires an identifier or string key"},
		{"block after known field", "string_field = \"x\"\n1 { }\n", "submessage block requires an identifier or string key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := pxf.Parse([]byte(c.src))
			require.Error(t, perr)
			assert.Contains(t, perr.Error(), c.want, "Parse")
			for _, discard := range []bool{false, true} {
				_, err := pxf.UnmarshalOptions{DiscardUnknown: discard}.UnmarshalDescriptor([]byte(c.src), desc)
				require.Error(t, err, "DiscardUnknown=%v", discard)
				assert.Equal(t, perr.Error(), err.Error(), "DiscardUnknown=%v", discard)
			}
		})
	}
}
