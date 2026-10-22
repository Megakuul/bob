package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/megakuul/bob/internal/api"
	logv1 "github.com/megakuul/bob/internal/gen/bob/log/v1"
	"github.com/megakuul/bob/internal/logstore"
	"github.com/megakuul/bob/internal/store"
)

// BuildJobs returns the job states of a build for the log feeds: the job list of the build
// record (control) and, for jobs that are not terminal there, owner and epoch from the job
// record (fleet). A job record that is gone was finalised: terminal.
func BuildJobs(control, fleet *store.Bucket) logstore.JobsFunc {
	return func(ctx context.Context, buildID string) ([]logstore.JobState, bool, error) {
		b, err := store.Get(ctx, control, &store.BuildRecord{ID: store.ID(buildID)})
		if err != nil {
			return nil, false, err
		}
		out := make([]logstore.JobState, 0, len(b.Jobs.Value()))
		for _, bj := range b.Jobs.Value() {
			js := logstore.JobState{ID: bj.ID, Owner: bj.Worker, Epoch: bj.Epoch, Terminal: terminalJob(bj.State)}
			if !js.Terminal {
				rec, err := store.Get(ctx, fleet, &store.JobRecord{ID: store.ID(bj.ID)})
				switch {
				case errors.Is(err, store.ErrNotFound):
					js.Terminal = true
				case err != nil:
					return nil, false, err
				default:
					js.Owner, js.Epoch, js.Terminal = rec.Owner.Value(), rec.Epoch.Value(), terminalJob(rec.State.Value())
				}
			}
			out = append(out, js)
		}
		return out, terminalBuild(b.State.Value()), nil
	}
}

func terminalJob(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

func terminalBuild(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "superseded", "skipped":
		return true
	}
	return false
}

// LogService serves bob.log.v1.LogService (SPEC §6.5.8): persisted logs from the lake, and
// FollowLog merging the lake with the live pull (ADR 0019). The API layer has already checked
// the caller's permission on the build's project (RESOLVE_BUILD).
type LogService struct {
	Sources *logstore.Sources
	Log     *slog.Logger
}

func (s *LogService) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// readErr maps reader errors to the API error model.
func (s *LogService) readErr(err error) error {
	if errors.Is(err, logstore.ErrBadRequest) {
		return api.InvalidArgument("", err.Error())
	}
	s.log().Warn("log read failed", "err", err)
	return api.Unavailable("log store busy, try again")
}

func toProto(ls []logstore.Line) []*logv1.LogLine {
	out := make([]*logv1.LogLine, len(ls))
	for i, l := range ls {
		out[i] = &logv1.LogLine{Package: l.Package, Seq: l.Seq, Attempt: l.Attempt, Stream: logv1.Stream(l.Stream),
			TsUnixNano: l.TS, Text: l.Text}
	}
	return out
}

// GetLog returns the persisted lines of a build or one package, ordered by (package, seq).
func (s *LogService) GetLog(ctx context.Context, req *connect.Request[logv1.GetLogRequest]) (*connect.Response[logv1.GetLogResponse], error) {
	m := req.Msg
	if m.GetLimit() < 0 || m.GetAfterSeq() < 0 {
		return nil, api.InvalidArgument("limit", "limit and after_seq must not be negative")
	}
	ls, more, err := s.Sources.Lake.Page(ctx, m.GetBuildId(), m.GetPackage(), m.GetAfterSeq(), int(m.GetLimit()))
	if err != nil {
		return nil, s.readErr(err)
	}
	return connect.NewResponse(&logv1.GetLogResponse{Lines: toProto(ls), Truncated: more}), nil
}

// SearchLog returns up to 1,000 lines of a build that contain the term.
func (s *LogService) SearchLog(ctx context.Context, req *connect.Request[logv1.SearchLogRequest]) (*connect.Response[logv1.SearchLogResponse], error) {
	m := req.Msg
	if n := len(m.GetTerm()); n < logstore.MinSearchTerm || n > logstore.MaxSearchTerm {
		return nil, api.InvalidArgument("term", "search term must be 3..200 bytes")
	}
	hits, more, err := s.Sources.Lake.Search(ctx, m.GetBuildId(), m.GetTerm(), m.GetPackage())
	if err != nil {
		return nil, s.readErr(err)
	}
	return connect.NewResponse(&logv1.SearchLogResponse{Hits: toProto(hits), Truncated: more}), nil
}

// FollowLog streams persisted and live lines after the cursor until every package of the
// build has its end marker, gap-free and duplicate-free.
func (s *LogService) FollowLog(ctx context.Context, req *connect.Request[logv1.FollowLogRequest], stream *connect.ServerStream[logv1.FollowLogResponse]) error {
	m := req.Msg
	if len(m.GetCursor()) > 10000 {
		return api.InvalidArgument("cursor", "too many cursors")
	}
	cur := make(map[string]logstore.Cursor, len(m.GetCursor()))
	for p, c := range m.GetCursor() {
		if c.GetSeq() < 0 {
			return api.InvalidArgument("cursor", "cursor seq must not be negative")
		}
		cur[p] = logstore.Cursor{Seq: c.GetSeq(), Attempt: c.GetAttempt()}
	}
	err := logstore.FollowPackage(ctx, s.Sources, m.GetBuildId(), m.GetPackage(), cur, func(b logstore.Batch) error {
		out := &logv1.FollowLogResponse{Lines: toProto(b.Lines), Done: b.Done, Delayed: b.Delayed}
		for _, r := range b.Restarts {
			out.Restarts = append(out.Restarts, &logv1.Restart{Package: r.Package, Attempt: r.Attempt, FromSeq: r.FromSeq})
		}
		for _, f := range b.End {
			out.End = append(out.End, &logv1.Final{Package: f.Package, LastSeq: f.LastSeq, Attempt: f.Attempt})
		}
		return stream.Send(out)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, logstore.ErrBadRequest):
		return api.InvalidArgument("", err.Error())
	case ctx.Err() != nil:
		return nil // the client left
	}
	return err
}
