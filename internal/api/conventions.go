package api

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// CRUD conventions of SPEC §6.1 shared by every service.

// Page sizes of List RPCs.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// PageSize validates a request's page_size: 0 means DefaultPageSize.
func PageSize(n int32) (int, error) {
	switch {
	case n == 0:
		return DefaultPageSize, nil
	case n < 0 || n > MaxPageSize:
		return 0, InvalidArgument("pageSize", fmt.Sprintf("page_size must be between 1 and %d", MaxPageSize))
	}
	return int(n), nil
}

// Page tokens are opaque to clients: "p1." + base64url of "<scope>:<position>". The scope
// (e.g. "builds/p1") binds a token to the list it came from, so a token of one list is
// refused by another instead of silently paging it from a wrong position.

const pageTokenPrefix = "p1."

// maxPageToken bounds what DecodePageToken accepts (hostile input).
const maxPageToken = 512

// EncodePageToken returns the token of position pos in the list named scope.
func EncodePageToken(scope string, pos uint64) string {
	return pageTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(scope+":"+strconv.FormatUint(pos, 10)))
}

// DecodePageToken returns the position of a token of list scope; "" is position 0.
func DecodePageToken(scope, tok string) (uint64, error) {
	if tok == "" {
		return 0, nil
	}
	bad := InvalidArgument("pageToken", "invalid page token")
	b64, ok := strings.CutPrefix(tok, pageTokenPrefix)
	if !ok || len(tok) > maxPageToken {
		return 0, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return 0, bad
	}
	s, pos, ok := strings.Cut(string(raw), ":")
	if !ok || s != scope {
		return 0, bad
	}
	n, err := strconv.ParseUint(pos, 10, 64)
	if err != nil {
		return 0, bad
	}
	return n, nil
}

// CheckETag compares the etag of an update or delete request with the stored one. An empty
// request etag is refused: every update is conditional (optimistic concurrency).
func CheckETag(field, requested, stored string) error {
	if requested == "" {
		return InvalidArgument(field, "etag is required")
	}
	if requested != stored {
		return Aborted("the resource was changed by someone else (etag mismatch); reload and retry")
	}
	return nil
}

// CheckMask validates an update_mask against the resource message: every path must name a
// field of msg and be one of updatable (top-level field names in proto spelling). An empty
// mask is refused.
func CheckMask(mask *fieldmaskpb.FieldMask, msg proto.Message, updatable ...string) error {
	if len(mask.GetPaths()) == 0 {
		return InvalidArgument("updateMask", "update_mask is required")
	}
	fields := msg.ProtoReflect().Descriptor().Fields()
	for _, p := range mask.GetPaths() {
		name := snake(p)
		if fields.ByName(protoreflect.Name(name)) == nil {
			return InvalidArgument("updateMask", fmt.Sprintf("unknown field %q in update_mask", p))
		}
		ok := false
		for _, u := range updatable {
			ok = ok || u == name
		}
		if !ok {
			return InvalidArgument("updateMask", fmt.Sprintf("field %q cannot be updated", p))
		}
	}
	return nil
}

// snake turns a protojson mask path ("displayName") into the proto name ("display_name").
func snake(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
