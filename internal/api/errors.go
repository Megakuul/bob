package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	commonv1 "github.com/megakuul/bob/internal/gen/bob/common/v1"
)

// The error model of SPEC §6.1 (R-API-150): every error is a connect error with exactly one
// bob.common.v1.ErrorDetail. Handlers return the constructors below; anything else that
// reaches the boundary becomes Internal with a reference id, details only in the server log.

// detailCode is the ErrorDetail.code of each connect code (§6.1 table). Codes the table does
// not name (Unimplemented, Unknown, DataLoss, …) are internal from the client's view.
var detailCode = map[connect.Code]string{
	connect.CodeInvalidArgument:    "BOB-API-1001",
	connect.CodeUnauthenticated:    "BOB-API-2001",
	connect.CodePermissionDenied:   "BOB-API-2002",
	connect.CodeNotFound:           "BOB-API-3001",
	connect.CodeAlreadyExists:      "BOB-API-3002",
	connect.CodeFailedPrecondition: "BOB-API-3003",
	connect.CodeAborted:            "BOB-API-3004",
	connect.CodeResourceExhausted:  "BOB-API-4001",
	connect.CodeUnavailable:        "BOB-API-5001",
	connect.CodeDeadlineExceeded:   "BOB-API-5001",
	connect.CodeCanceled:           "BOB-API-5001",
}

const internalDetail = "BOB-API-9001"

func newError(code connect.Code, field, msg string) *connect.Error {
	e := connect.NewError(code, errors.New(msg))
	dc, ok := detailCode[code]
	if !ok {
		dc = internalDetail
	}
	addDetail(e, &commonv1.ErrorDetail{Code: dc, Field: field})
	return e
}

func addDetail(e *connect.Error, d *commonv1.ErrorDetail) {
	if det, err := connect.NewErrorDetail(d); err == nil {
		e.AddDetail(det)
	}
}

// InvalidArgument names the offending request field (protojson path, e.g. "role.id").
func InvalidArgument(field, msg string) error {
	return newError(connect.CodeInvalidArgument, field, msg)
}

// Unauthenticated is a missing, invalid or expired credential.
func Unauthenticated(msg string) error { return newError(connect.CodeUnauthenticated, "", msg) }

// PermissionDenied is the answer when the principal has some permission on the project but not
// this method. project "" is a global method.
func PermissionDenied(method, project string) error {
	msg := "permission denied: " + method
	if project != "" {
		msg += " on project " + project
	}
	return newError(connect.CodePermissionDenied, "", msg)
}

// NotFound is "<what> not found"; it is also the answer without any permission on the
// project, so existence never leaks.
func NotFound(what string) error { return newError(connect.CodeNotFound, "", what+" not found") }

// AlreadyExists is a create on an existing id.
func AlreadyExists(msg string) error { return newError(connect.CodeAlreadyExists, "", msg) }

// FailedPrecondition is a state that forbids the action.
func FailedPrecondition(msg string) error { return newError(connect.CodeFailedPrecondition, "", msg) }

// Aborted is an etag mismatch or a lost CAS race.
func Aborted(msg string) error { return newError(connect.CodeAborted, "", msg) }

// ResourceExhausted is a rate or stream limit.
func ResourceExhausted(msg string) error {
	return newError(connect.CodeResourceExhausted, "", msg)
}

// Unavailable is storage unavailable or a timeout.
func Unavailable(msg string) error { return newError(connect.CodeUnavailable, "", msg) }

// normalize turns whatever a handler or the authz layer returned into the error model.
func normalize(ctx context.Context, log *slog.Logger, procedure string, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch n := len(ce.Details()); {
		case ce.Code() == connect.CodeUnimplemented:
			// A method whose milestone has not landed yet; the text is the harness's
			// "not implemented:" marker (tools/ci acceptance stage).
			return newError(connect.CodeUnimplemented, "", "not implemented: "+strings.TrimPrefix(procedure, "/"))
		case n == 1 && ce.Code() != connect.CodeInternal && ce.Code() != connect.CodeUnknown:
			return ce
		case ce.Code() == connect.CodeInternal || ce.Code() == connect.CodeUnknown:
			return internal(ctx, log, procedure, err)
		default:
			// A connect error without our detail (e.g. Unimplemented from a stub, a protocol
			// error) keeps its code and message and gets the one detail.
			return newError(ce.Code(), "", ce.Message())
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if ctx.Err() != nil {
			return newError(connect.CodeUnavailable, "", "request cancelled or timed out")
		}
	}
	return internal(ctx, log, procedure, err)
}

// internal logs err under a fresh reference and returns the opaque answer.
func internal(ctx context.Context, log *slog.Logger, procedure string, err error) error {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	ref := hex.EncodeToString(b)
	log.ErrorContext(ctx, "internal error", "ref", ref, "procedure", procedure, "err", err.Error())
	return newError(connect.CodeInternal, "", fmt.Sprintf("internal error (ref %s)", ref))
}
