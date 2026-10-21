// Package msg builds msg.v1 notes with bob-generated protobuf code.
package msg

import (
	msgv1 "example.com/api/gen/proto/msg/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// New returns a note with a fresh id.
func New(body string, checksum uint32) *msgv1.Note {
	return &msgv1.Note{Id: uuid.NewString(), Body: body, Checksum: checksum}
}

// Size is the encoded size of n.
func Size(n *msgv1.Note) int { return proto.Size(n) }
