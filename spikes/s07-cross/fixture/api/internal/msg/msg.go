// Package msg builds the greeting message (a library package of the fixture, added in S2 so a
// one-file change in a non-main package can be measured).
package msg

import (
	"example.com/api/gen/greetingv1"
	"github.com/google/uuid"
)

// New returns a greeting with a fresh id.
func New(text, digest string) *greetingv1.Greeting {
	return &greetingv1.Greeting{Id: uuid.NewString(), Text: text, Digest: digest}
}
// incr 1792532087353213796
// incr 1792532099645662939
// incr 1792532133366134813
// incr 1792532138122130526
// incr 1792532180196138295
// incr 1792532195456779392
// incr 1792532209628649460
// incr 1792532544328588829
